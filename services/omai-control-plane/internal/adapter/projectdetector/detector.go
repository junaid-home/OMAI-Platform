// Package projectdetector derives shell-free preview plans from bounded,
// runtime-relevant workspace metadata. Detection never executes repository
// code; execution remains behind the workspace executor port.
package projectdetector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/omai/backend/internal/domain"
)

const (
	maxManifestBytes = 2 << 20
	maxManifests     = 256
	maxDepth         = 5
)

var ignoredDirectories = map[string]struct{}{
	".git": {}, ".cache": {}, ".next": {}, ".omai-preview": {}, ".turbo": {}, ".venv": {},
	"build": {}, "dist": {}, "node_modules": {}, "target": {}, "vendor": {}, "venv": {},
}

type Detector struct{ now func() time.Time }

func New() *Detector { return &Detector{now: func() time.Time { return time.Now().UTC() }} }

func (d *Detector) Analyze(ctx context.Context, workspace domain.Workspace) (domain.RuntimePlan, error) {
	root, err := cleanRoot(workspace.Root)
	if err != nil {
		return domain.RuntimePlan{}, err
	}
	fingerprint, err := fingerprint(ctx, root)
	if err != nil {
		return domain.RuntimePlan{}, fmt.Errorf("fingerprint preview metadata: %w", err)
	}
	if plan, found, err := explicitPlan(ctx, root, workspace.ID, fingerprint, d.now()); err != nil {
		return domain.RuntimePlan{}, err
	} else if found {
		return plan, validatePlan(root, plan)
	}

	var services []domain.RuntimeServicePlan
	var evidence []domain.DetectionEvidence
	detectors := []func(context.Context, string) ([]domain.RuntimeServicePlan, []domain.DetectionEvidence, error){
		detectNode, detectGo, detectPython, detectRust, detectJava, detectPHP,
	}
	for _, detect := range detectors {
		found, reasons, detectErr := detect(ctx, root)
		if detectErr != nil {
			if errors.Is(detectErr, context.Canceled) || errors.Is(detectErr, context.DeadlineExceeded) {
				return domain.RuntimePlan{}, detectErr
			}
			continue
		}
		services = append(services, found...)
		evidence = append(evidence, reasons...)
	}
	if len(services) == 0 {
		found, reasons, detectErr := detectStatic(ctx, root)
		if detectErr != nil {
			return domain.RuntimePlan{}, detectErr
		}
		services, evidence = append(services, found...), append(evidence, reasons...)
	}
	services = deduplicate(services)
	if len(services) == 0 {
		return domain.RuntimePlan{}, fmt.Errorf("%w: no runnable project detected; add .omai/runtime.json", domain.ErrNotFound)
	}
	plan := domain.RuntimePlan{
		Version: 1, WorkspaceID: workspace.ID, Fingerprint: fingerprint, Source: domain.RuntimePlanSourceDetected,
		Primary: choosePrimary(services, evidence), Services: services, Evidence: evidence, AnalyzedAt: d.now(),
	}
	return plan, validatePlan(root, plan)
}

type explicitConfig struct {
	Version  int32             `json:"version"`
	Primary  string            `json:"primary"`
	Services []explicitService `json:"services"`
}

type explicitService struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	WorkingDir     string           `json:"workingDir"`
	Runtime        string           `json:"runtime"`
	RuntimeVersion string           `json:"runtimeVersion"`
	Framework      string           `json:"framework"`
	PackageManager string           `json:"packageManager"`
	Install        *explicitCommand `json:"install"`
	Run            explicitCommand  `json:"run"`
	Preview        bool             `json:"preview"`
	ExpectedPorts  []uint32         `json:"expectedPorts"`
	DependsOn      []string         `json:"dependsOn"`
}

type explicitCommand struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func explicitPlan(ctx context.Context, root, workspaceID, fingerprint string, now time.Time) (domain.RuntimePlan, bool, error) {
	for _, relative := range []string{filepath.Join(".omai", "runtime.json"), "omai.runtime.json"} {
		if err := ctx.Err(); err != nil {
			return domain.RuntimePlan{}, false, err
		}
		data, err := readBounded(filepath.Join(root, relative), 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return domain.RuntimePlan{}, false, err
		}
		var config explicitConfig
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return domain.RuntimePlan{}, false, fmt.Errorf("%w: invalid %s: %v", domain.ErrInvalid, filepath.Base(relative), err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return domain.RuntimePlan{}, false, fmt.Errorf("%w: trailing data in %s", domain.ErrInvalid, filepath.Base(relative))
		}
		if config.Version == 0 {
			config.Version = 1
		}
		plan := domain.RuntimePlan{
			Version: config.Version, WorkspaceID: workspaceID, Fingerprint: fingerprint, Source: domain.RuntimePlanSourceExplicit,
			Primary: config.Primary, Evidence: []domain.DetectionEvidence{{Detector: "explicit", Path: filepath.ToSlash(relative), Reason: "explicit OMAI runtime configuration", Score: 100}}, AnalyzedAt: now,
		}
		for _, service := range config.Services {
			converted := domain.RuntimeServicePlan{
				ID: service.ID, Name: service.Name, WorkingDir: service.WorkingDir, Runtime: service.Runtime,
				RuntimeVersion: service.RuntimeVersion, Framework: service.Framework, PackageManager: service.PackageManager,
				Run: command(service.Run), Preview: service.Preview, ExpectedPorts: append([]uint32(nil), service.ExpectedPorts...), DependsOn: append([]string(nil), service.DependsOn...),
			}
			if service.Install != nil {
				install := command(*service.Install)
				converted.Install = &install
			}
			plan.Services = append(plan.Services, converted)
		}
		if plan.Primary == "" && len(plan.Services) > 0 {
			plan.Primary = plan.Services[0].ID
		}
		return plan, true, nil
	}
	return domain.RuntimePlan{}, false, nil
}

func command(value explicitCommand) domain.RuntimeCommand {
	return domain.RuntimeCommand{Command: value.Command, Args: append([]string(nil), value.Args...), Env: cloneMap(value.Env)}
}

type packageManifest struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Engines         map[string]string `json:"engines"`
}

func detectNode(ctx context.Context, root string) ([]domain.RuntimeServicePlan, []domain.DetectionEvidence, error) {
	paths, err := findFiles(ctx, root, map[string]struct{}{"package.json": {}}, maxDepth, 64)
	if err != nil {
		return nil, nil, err
	}
	var services []domain.RuntimeServicePlan
	var evidence []domain.DetectionEvidence
	for _, path := range paths {
		data, err := readBounded(path, maxManifestBytes)
		if err != nil {
			continue
		}
		var manifest packageManifest
		if json.Unmarshal(data, &manifest) != nil {
			continue
		}
		script := firstScript(manifest.Scripts)
		if script == "" {
			continue
		}
		directory := filepath.Dir(path)
		relative := relativeDir(root, directory)
		framework := nodeFramework(directory, manifest)
		manager, install := packageManager(root, directory)
		args := []string{"run", script}
		switch framework {
		case "vite", "astro":
			args = append(args, "--", "--host", "{{host}}", "--port", "{{port}}", "--strictPort")
		case "next":
			args = append(args, "--", "--hostname", "{{host}}", "--port", "{{port}}")
		case "angular":
			args = append(args, "--", "--host", "{{host}}", "--port", "{{port}}")
		}
		service := domain.RuntimeServicePlan{
			ID: serviceID("node", relative), Name: serviceName(relative, "web"), WorkingDir: relative, Runtime: "node",
			RuntimeVersion: manifest.Engines["node"], Framework: framework, PackageManager: manager,
			Install: &install, Run: domain.RuntimeCommand{Command: manager, Args: args, Env: map[string]string{"HOST": "{{host}}", "PORT": "{{port}}"}}, Preview: true,
		}
		services = append(services, service)
		evidence = append(evidence, reason("node", filepath.ToSlash(filepath.Join(relative, "package.json")), "runnable package script and framework="+framework, 95))
	}
	return services, evidence, nil
}

func detectGo(ctx context.Context, root string) ([]domain.RuntimeServicePlan, []domain.DetectionEvidence, error) {
	paths, err := findFiles(ctx, root, map[string]struct{}{"go.mod": {}}, maxDepth, 32)
	if err != nil {
		return nil, nil, err
	}
	var services []domain.RuntimeServicePlan
	var evidence []domain.DetectionEvidence
	for _, path := range paths {
		directory := filepath.Dir(path)
		if !hasGoMain(directory) {
			continue
		}
		relative := relativeDir(root, directory)
		services = append(services, domain.RuntimeServicePlan{
			ID: serviceID("go", relative), Name: serviceName(relative, "go-web"), WorkingDir: relative, Runtime: "go", Framework: "go",
			Run:     domain.RuntimeCommand{Command: "go", Args: []string{"run", "."}, Env: map[string]string{"HOST": "{{host}}", "PORT": "{{port}}", "ADDR": "{{host}}:{{port}}"}},
			Preview: true, ExpectedPorts: []uint32{8080, 3000},
		})
		evidence = append(evidence, reason("go", filepath.ToSlash(filepath.Join(relative, "go.mod")), "Go module with package main", 85))
	}
	return services, evidence, nil
}

func detectPython(ctx context.Context, root string) ([]domain.RuntimeServicePlan, []domain.DetectionEvidence, error) {
	paths, err := findFiles(ctx, root, map[string]struct{}{"pyproject.toml": {}, "requirements.txt": {}, "manage.py": {}, "app.py": {}, "main.py": {}}, maxDepth, 96)
	if err != nil {
		return nil, nil, err
	}
	directories := map[string]struct{}{}
	for _, path := range paths {
		directories[filepath.Dir(path)] = struct{}{}
	}
	var services []domain.RuntimeServicePlan
	var evidence []domain.DetectionEvidence
	for directory := range directories {
		relative := relativeDir(root, directory)
		var run domain.RuntimeCommand
		framework := "python"
		switch {
		case regular(filepath.Join(directory, "manage.py")):
			framework = "django"
			run = domain.RuntimeCommand{Command: "python3", Args: []string{"manage.py", "runserver", "{{host}}:{{port}}"}}
		case containsAny(filepath.Join(directory, "main.py"), "FastAPI(", "from fastapi"):
			framework = "fastapi"
			run = domain.RuntimeCommand{Command: "python3", Args: []string{"-m", "uvicorn", "main:app", "--host", "{{host}}", "--port", "{{port}}"}}
		case regular(filepath.Join(directory, "app.py")):
			framework = "flask"
			run = domain.RuntimeCommand{Command: "python3", Args: []string{"-m", "flask", "--app", "app", "run", "--host", "{{host}}", "--port", "{{port}}"}}
		default:
			continue
		}
		run.Env = map[string]string{"HOST": "{{host}}", "PORT": "{{port}}", "PYTHONPATH": ".omai-preview/python"}
		var install *domain.RuntimeCommand
		if regular(filepath.Join(directory, "requirements.txt")) {
			value := domain.RuntimeCommand{Command: "python3", Args: []string{"-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--target", ".omai-preview/python", "-r", "requirements.txt"}}
			install = &value
		} else if regular(filepath.Join(directory, "pyproject.toml")) {
			value := domain.RuntimeCommand{Command: "python3", Args: []string{"-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--target", ".omai-preview/python", "."}}
			install = &value
		}
		services = append(services, domain.RuntimeServicePlan{ID: serviceID("python", relative), Name: serviceName(relative, "python-web"), WorkingDir: relative, Runtime: "python", Framework: framework, Install: install, Run: run, Preview: true, ExpectedPorts: []uint32{8000, 5000}})
		evidence = append(evidence, reason("python", relative, "detected "+framework+" application", 85))
	}
	return services, evidence, nil
}

func detectRust(ctx context.Context, root string) ([]domain.RuntimeServicePlan, []domain.DetectionEvidence, error) {
	paths, err := findFiles(ctx, root, map[string]struct{}{"Cargo.toml": {}}, maxDepth, 32)
	if err != nil {
		return nil, nil, err
	}
	var services []domain.RuntimeServicePlan
	var evidence []domain.DetectionEvidence
	for _, path := range paths {
		directory := filepath.Dir(path)
		if !regular(filepath.Join(directory, "src", "main.rs")) {
			continue
		}
		relative := relativeDir(root, directory)
		services = append(services, domain.RuntimeServicePlan{ID: serviceID("rust", relative), Name: serviceName(relative, "rust-web"), WorkingDir: relative, Runtime: "rust", Framework: "rust", Run: domain.RuntimeCommand{Command: "cargo", Args: []string{"run"}, Env: map[string]string{"HOST": "{{host}}", "PORT": "{{port}}", "ROCKET_ADDRESS": "{{host}}", "ROCKET_PORT": "{{port}}"}}, Preview: true, ExpectedPorts: []uint32{8000, 8080, 3000}})
		evidence = append(evidence, reason("rust", filepath.ToSlash(filepath.Join(relative, "Cargo.toml")), "Rust binary crate", 75))
	}
	return services, evidence, nil
}

func detectJava(ctx context.Context, root string) ([]domain.RuntimeServicePlan, []domain.DetectionEvidence, error) {
	paths, err := findFiles(ctx, root, map[string]struct{}{"pom.xml": {}, "build.gradle": {}, "build.gradle.kts": {}}, maxDepth, 32)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]struct{}{}
	var services []domain.RuntimeServicePlan
	var evidence []domain.DetectionEvidence
	for _, path := range paths {
		directory := filepath.Dir(path)
		if _, ok := seen[directory]; ok {
			continue
		}
		seen[directory] = struct{}{}
		relative := relativeDir(root, directory)
		var run domain.RuntimeCommand
		if filepath.Base(path) == "pom.xml" {
			command := "mvn"
			if regular(filepath.Join(directory, "mvnw")) {
				command = "./mvnw"
			}
			run = domain.RuntimeCommand{Command: command, Args: []string{"spring-boot:run", "-Dspring-boot.run.arguments=--server.address={{host}} --server.port={{port}}"}}
		} else {
			command := "gradle"
			if regular(filepath.Join(directory, "gradlew")) {
				command = "./gradlew"
			}
			run = domain.RuntimeCommand{Command: command, Args: []string{"bootRun", "--args=--server.address={{host}} --server.port={{port}}"}}
		}
		services = append(services, domain.RuntimeServicePlan{ID: serviceID("java", relative), Name: serviceName(relative, "java-web"), WorkingDir: relative, Runtime: "java", Framework: "spring", Run: run, Preview: true, ExpectedPorts: []uint32{8080}})
		evidence = append(evidence, reason("java", filepath.ToSlash(filepath.Join(relative, filepath.Base(path))), "Java web build", 70))
	}
	return services, evidence, nil
}

func detectPHP(ctx context.Context, root string) ([]domain.RuntimeServicePlan, []domain.DetectionEvidence, error) {
	paths, err := findFiles(ctx, root, map[string]struct{}{"composer.json": {}}, maxDepth, 32)
	if err != nil {
		return nil, nil, err
	}
	var services []domain.RuntimeServicePlan
	var evidence []domain.DetectionEvidence
	for _, path := range paths {
		directory := filepath.Dir(path)
		relative := relativeDir(root, directory)
		documentRoot := "."
		if info, err := os.Stat(filepath.Join(directory, "public")); err == nil && info.IsDir() {
			documentRoot = "public"
		}
		install := domain.RuntimeCommand{Command: "composer", Args: []string{"install", "--no-interaction", "--prefer-dist"}}
		services = append(services, domain.RuntimeServicePlan{ID: serviceID("php", relative), Name: serviceName(relative, "php-web"), WorkingDir: relative, Runtime: "php", Framework: "php", Install: &install, Run: domain.RuntimeCommand{Command: "php", Args: []string{"-S", "{{host}}:{{port}}", "-t", documentRoot}}, Preview: true})
		evidence = append(evidence, reason("php", filepath.ToSlash(filepath.Join(relative, "composer.json")), "PHP Composer application", 70))
	}
	return services, evidence, nil
}

func detectStatic(ctx context.Context, root string) ([]domain.RuntimeServicePlan, []domain.DetectionEvidence, error) {
	paths, err := findFiles(ctx, root, map[string]struct{}{"index.html": {}}, maxDepth, 32)
	if err != nil {
		return nil, nil, err
	}
	var services []domain.RuntimeServicePlan
	var evidence []domain.DetectionEvidence
	for _, path := range paths {
		relative := relativeDir(root, filepath.Dir(path))
		services = append(services, domain.RuntimeServicePlan{ID: serviceID("static", relative), Name: serviceName(relative, "static-web"), WorkingDir: relative, Runtime: "static", Framework: "static", Run: domain.RuntimeCommand{Command: "python3", Args: []string{"-m", "http.server", "{{port}}", "--bind", "{{host}}"}}, Preview: true})
		evidence = append(evidence, reason("static", filepath.ToSlash(filepath.Join(relative, "index.html")), "static index.html", 50))
	}
	return services, evidence, nil
}

func validatePlan(root string, plan domain.RuntimePlan) error {
	if plan.Version != 1 || plan.WorkspaceID == "" || plan.Primary == "" || len(plan.Services) == 0 || len(plan.Services) > 64 {
		return fmt.Errorf("%w: incomplete runtime plan", domain.ErrInvalid)
	}
	byID := make(map[string]domain.RuntimeServicePlan, len(plan.Services))
	for _, service := range plan.Services {
		if !safeIdentifier(service.ID) || !safeCommand(service.Run.Command) || len(service.Run.Args) > 256 || len(service.Run.Env) > 64 || len(service.DependsOn) > 64 {
			return fmt.Errorf("%w: unsafe runtime service", domain.ErrInvalid)
		}
		if _, exists := byID[service.ID]; exists {
			return fmt.Errorf("%w: duplicate runtime service %q", domain.ErrConflict, service.ID)
		}
		if err := containedDirectory(root, service.WorkingDir); err != nil {
			return err
		}
		if service.Install != nil && !safeCommand(service.Install.Command) {
			return fmt.Errorf("%w: unsafe install command", domain.ErrInvalid)
		}
		for _, argument := range append(append([]string(nil), service.Run.Args...), installArgs(service.Install)...) {
			if len(argument) > 64<<10 || strings.ContainsRune(argument, '\x00') {
				return fmt.Errorf("%w: unsafe runtime argument", domain.ErrInvalid)
			}
		}
		byID[service.ID] = service
	}
	primary, ok := byID[plan.Primary]
	if !ok || !primary.Preview {
		return fmt.Errorf("%w: primary preview service not found", domain.ErrInvalid)
	}
	state := map[string]uint8{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("%w: runtime dependency cycle", domain.ErrConflict)
		}
		if state[id] == 2 {
			return nil
		}
		service, ok := byID[id]
		if !ok {
			return fmt.Errorf("%w: unknown runtime dependency", domain.ErrInvalid)
		}
		state[id] = 1
		for _, dependency := range service.DependsOn {
			if dependency == id {
				return fmt.Errorf("%w: self dependency", domain.ErrInvalid)
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	return visit(plan.Primary)
}

func cleanRoot(value string) (string, error) {
	root := filepath.Clean(value)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: workspace root", domain.ErrInvalid)
	}
	return root, nil
}

func findFiles(ctx context.Context, root string, names map[string]struct{}, depth, limit int) ([]string, error) {
	var result []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if relative != "." {
				if _, ignored := ignoredDirectories[entry.Name()]; ignored || relativeDepth(relative) > depth {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if relativeDepth(filepath.Dir(relative)) <= depth {
			if _, wanted := names[entry.Name()]; wanted {
				result = append(result, path)
				if len(result) >= limit {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if errors.Is(err, fs.SkipAll) {
		err = nil
	}
	sort.Strings(result)
	return result, err
}

func fingerprint(ctx context.Context, root string) (string, error) {
	names := map[string]struct{}{
		"package.json": {}, "package-lock.json": {}, "pnpm-lock.yaml": {}, "yarn.lock": {}, "bun.lock": {}, "bun.lockb": {},
		"go.mod": {}, "go.work": {}, "Cargo.toml": {}, "pyproject.toml": {}, "requirements.txt": {}, "uv.lock": {},
		"pom.xml": {}, "build.gradle": {}, "build.gradle.kts": {}, "composer.json": {}, "composer.lock": {},
		"vite.config.ts": {}, "vite.config.js": {}, "vite.config.mjs": {}, "next.config.js": {}, "next.config.mjs": {},
		"astro.config.mjs": {}, "astro.config.ts": {}, "omai.runtime.json": {}, "index.html": {},
	}
	paths, err := findFiles(ctx, root, names, maxDepth, maxManifests)
	if err != nil {
		return "", err
	}
	explicit := filepath.Join(root, ".omai", "runtime.json")
	if regular(explicit) {
		paths = append(paths, explicit)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		data, err := readBounded(path, maxManifestBytes)
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: manifest must be a regular file", domain.ErrInvalid)
	}
	// #nosec G304 -- every caller obtains path from the bounded, symlink-skipping
	// workspace walk or one of the two fixed explicit-manifest locations.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: manifest exceeds size limit", domain.ErrInvalid)
	}
	return data, nil
}

func hasGoMain(directory string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := readBounded(filepath.Join(directory, entry.Name()), 1<<20)
		if err == nil && strings.Contains(string(data), "package main") && strings.Contains(string(data), "func main(") {
			return true
		}
	}
	return false
}

func nodeFramework(directory string, manifest packageManifest) string {
	has := func(name string) bool {
		_, ok := manifest.Dependencies[name]
		if !ok {
			_, ok = manifest.DevDependencies[name]
		}
		return ok
	}
	switch {
	case has("next"):
		return "next"
	case has("astro"):
		return "astro"
	case has("vite") || regular(filepath.Join(directory, "vite.config.ts")) || regular(filepath.Join(directory, "vite.config.js")):
		return "vite"
	case has("@angular/core"):
		return "angular"
	case has("@sveltejs/kit") || has("svelte"):
		return "svelte"
	case has("solid-js"):
		return "solidjs"
	case has("react"):
		return "react"
	default:
		return "node"
	}
}

func packageManager(root, directory string) (string, domain.RuntimeCommand) {
	for _, base := range []string{directory, root} {
		if regular(filepath.Join(base, "pnpm-lock.yaml")) {
			return "pnpm", domain.RuntimeCommand{Command: "pnpm", Args: []string{"install", "--frozen-lockfile"}}
		}
		if regular(filepath.Join(base, "bun.lock")) || regular(filepath.Join(base, "bun.lockb")) {
			return "bun", domain.RuntimeCommand{Command: "bun", Args: []string{"install", "--frozen-lockfile"}}
		}
		if regular(filepath.Join(base, "yarn.lock")) {
			return "yarn", domain.RuntimeCommand{Command: "yarn", Args: []string{"install", "--immutable"}}
		}
		if regular(filepath.Join(base, "package-lock.json")) {
			return "npm", domain.RuntimeCommand{Command: "npm", Args: []string{"ci", "--ignore-scripts=false"}}
		}
	}
	return "npm", domain.RuntimeCommand{Command: "npm", Args: []string{"install"}}
}

func firstScript(scripts map[string]string) string {
	for _, name := range []string{"dev", "start", "serve", "preview"} {
		if strings.TrimSpace(scripts[name]) != "" {
			return name
		}
	}
	return ""
}

func containsAny(path string, values ...string) bool {
	data, err := readBounded(path, 1<<20)
	if err != nil {
		return false
	}
	for _, value := range values {
		if strings.Contains(string(data), value) {
			return true
		}
	}
	return false
}

func containedDirectory(root, relative string) error {
	if filepath.IsAbs(relative) || strings.ContainsRune(relative, '\x00') {
		return fmt.Errorf("%w: runtime working directory", domain.ErrInvalid)
	}
	joined := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return fmt.Errorf("%w: runtime working directory", domain.ErrInvalid)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: runtime working directory escapes workspace", domain.ErrForbidden)
	}
	return nil
}

func choosePrimary(services []domain.RuntimeServicePlan, evidence []domain.DetectionEvidence) string {
	best, bestScore := services[0].ID, -1
	for _, service := range services {
		score := 0
		if service.Preview {
			score += 100
		}
		if service.WorkingDir == "" {
			score += 20
		}
		switch service.Framework {
		case "vite", "next", "astro", "solidjs", "react", "svelte", "angular":
			score += 30
		}
		for _, item := range evidence {
			if item.Path == service.WorkingDir || strings.HasPrefix(item.Path, strings.TrimSuffix(service.WorkingDir, "/")+"/") {
				score += int(item.Score / 10)
			}
		}
		if score > bestScore {
			best, bestScore = service.ID, score
		}
	}
	return best
}

func deduplicate(services []domain.RuntimeServicePlan) []domain.RuntimeServicePlan {
	seen := map[string]struct{}{}
	result := make([]domain.RuntimeServicePlan, 0, len(services))
	for _, service := range services {
		key := filepath.ToSlash(filepath.Clean(service.WorkingDir)) + "\x00" + service.Runtime
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, service)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].WorkingDir == "" {
			return true
		}
		if result[right].WorkingDir == "" {
			return false
		}
		return result[left].ID < result[right].ID
	})
	return result
}

func regular(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func relativeDir(root, directory string) string {
	relative, _ := filepath.Rel(root, directory)
	if relative == "." {
		return ""
	}
	return filepath.ToSlash(relative)
}

func relativeDepth(relative string) int {
	if relative == "." || relative == "" {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(relative), "/"))
}

func serviceID(runtime, relative string) string {
	relative = strings.Trim(filepath.ToSlash(relative), "/")
	if relative == "" {
		return runtime + "-root"
	}
	return runtime + "-" + strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(relative)
}

func serviceName(relative, fallback string) string {
	if relative == "" {
		return fallback
	}
	return filepath.Base(filepath.FromSlash(relative))
}

func reason(detector, path, message string, score int32) domain.DetectionEvidence {
	return domain.DetectionEvidence{Detector: detector, Path: path, Reason: message, Score: score}
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character == '-' || character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func safeCommand(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\r\n\x00")
}

func installArgs(command *domain.RuntimeCommand) []string {
	if command == nil {
		return nil
	}
	return command.Args
}

func cloneMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
