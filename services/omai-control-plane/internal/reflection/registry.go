package reflection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

type Tool struct {
	Descriptor *uabv1.ReflectedTool
	Input      protoreflect.MessageDescriptor
}

type Registry struct {
	byProcedure map[string]Tool
	byName      map[string]Tool
	tools       []*uabv1.ReflectedTool
	etag        string
}

func Build() (*Registry, error) {
	registry := &Registry{byProcedure: make(map[string]Tool), byName: make(map[string]Tool)}
	var buildErr error
	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		services := file.Services()
		for serviceIndex := 0; serviceIndex < services.Len(); serviceIndex++ {
			service := services.Get(serviceIndex)
			methods := service.Methods()
			for methodIndex := 0; methodIndex < methods.Len(); methodIndex++ {
				method := methods.Get(methodIndex)
				options, _ := method.Options().(*descriptorpb.MethodOptions)
				if options == nil || !proto.HasExtension(options, uabv1.E_Tool) {
					continue
				}
				metadata, ok := proto.GetExtension(options, uabv1.E_Tool).(*uabv1.ToolOptions)
				if !ok || metadata == nil || !metadata.GetEnabled() {
					continue
				}
				procedure := fmt.Sprintf("/%s/%s", service.FullName(), method.Name())
				tool, err := buildTool(procedure, method, metadata)
				if err != nil {
					buildErr = err
					return false
				}
				if _, exists := registry.byProcedure[procedure]; exists {
					buildErr = fmt.Errorf("duplicate reflected procedure %s", procedure)
					return false
				}
				registry.byProcedure[procedure] = tool
				if _, exists := registry.byName[tool.Descriptor.GetName()]; exists {
					buildErr = fmt.Errorf("duplicate reflected tool name %s", tool.Descriptor.GetName())
					return false
				}
				registry.byName[tool.Descriptor.GetName()] = tool
				registry.tools = append(registry.tools, tool.Descriptor)
			}
		}
		return buildErr == nil
	})
	if buildErr != nil {
		return nil, buildErr
	}
	if len(registry.tools) == 0 {
		return nil, fmt.Errorf("no enabled OMAI tools were found in compiled descriptors")
	}
	sort.Slice(registry.tools, func(left, right int) bool {
		return registry.tools[left].GetName() < registry.tools[right].GetName()
	})
	fingerprint := sha256.New()
	for _, tool := range registry.tools {
		_, _ = fmt.Fprintf(fingerprint, "%s\x00%s\x00%s\x00%d\x00%d\x00%v\x00%v\x00%t\x00%t\n",
			tool.GetName(), tool.GetVersion(), tool.GetGrpcMethod(), tool.GetRisk(),
			tool.GetConfirmation(), tool.GetPermissions(), tool.GetModalities(), tool.GetClientStreaming(), tool.GetServerStreaming())
	}
	registry.etag = hex.EncodeToString(fingerprint.Sum(nil))
	return registry, nil
}

func buildTool(procedure string, method protoreflect.MethodDescriptor, metadata *uabv1.ToolOptions) (Tool, error) {
	if metadata.GetPublicName() == "" || metadata.GetExecutor() == "" {
		return Tool{}, fmt.Errorf("tool annotation on %s requires public_name and executor", procedure)
	}
	if metadata.GetVersion() == "" {
		return Tool{}, fmt.Errorf("tool annotation on %s requires a version", procedure)
	}
	if err := validateRequiredFields(method.Input(), metadata.GetRequiredFields()); err != nil {
		return Tool{}, fmt.Errorf("tool annotation on %s: %w", procedure, err)
	}
	schema, err := json.Marshal(messageSchema(method.Input(), metadata.GetRequiredFields(), make(map[protoreflect.FullName]bool)))
	if err != nil {
		return Tool{}, fmt.Errorf("encode input schema for %s: %w", procedure, err)
	}
	return Tool{
		Descriptor: &uabv1.ReflectedTool{
			Name:            metadata.GetPublicName(),
			Version:         metadata.GetVersion(),
			Description:     metadata.GetDescription(),
			Risk:            metadata.GetRisk(),
			Confirmation:    metadata.GetConfirmation(),
			Permissions:     append([]string(nil), metadata.GetPermissions()...),
			TimeoutMs:       metadata.GetTimeoutMs(),
			Idempotent:      metadata.GetIdempotent(),
			Executor:        metadata.GetExecutor(),
			GrpcMethod:      procedure,
			InputSchemaJson: schema,
			RequiredFields:  append([]string(nil), metadata.GetRequiredFields()...),
			Modalities:      append([]string(nil), metadata.GetModalities()...),
			ClientStreaming: method.IsStreamingClient(),
			ServerStreaming: method.IsStreamingServer(),
		},
		Input: method.Input(),
	}, nil
}

func (r *Registry) Resolve(name, version string) (Tool, bool) {
	tool, ok := r.byName[name]
	if !ok || (version != "" && version != tool.Descriptor.GetVersion()) {
		return Tool{}, false
	}
	return Tool{Descriptor: proto.Clone(tool.Descriptor).(*uabv1.ReflectedTool), Input: tool.Input}, true
}

func (r *Registry) VoiceTools(principalPermissions []string) ([]*uabv1.ReflectedTool, string) {
	allowed := make(map[string]struct{}, len(principalPermissions))
	for _, permission := range principalPermissions {
		allowed[permission] = struct{}{}
	}
	result := make([]*uabv1.ReflectedTool, 0)
	for _, candidate := range r.tools {
		if candidate.GetClientStreaming() || candidate.GetServerStreaming() || !voiceEligible(candidate) {
			continue
		}
		permitted := true
		for _, permission := range candidate.GetPermissions() {
			if _, all := allowed["*"]; !all {
				if _, ok := allowed[permission]; !ok {
					permitted = false
					break
				}
			}
		}
		if permitted {
			result = append(result, proto.Clone(candidate).(*uabv1.ReflectedTool))
		}
	}
	return result, r.etag
}

func voiceEligible(tool *uabv1.ReflectedTool) bool {
	if len(tool.GetModalities()) != 0 {
		for _, modality := range tool.GetModalities() {
			if modality == "voice" || modality == "audio" {
				return true
			}
		}
		return false
	}
	switch tool.GetExecutor() {
	case "agent.runtime", "go.voice-control", "go.reflection", "go.event-store", "go.preview-gateway", "go.process", "go.lsp":
		return false
	default:
		return tool.GetName() != "system_health"
	}
}

func ValidateArguments(tool Tool, raw []byte) error {
	message := dynamicpb.NewMessage(tool.Input)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, message); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	fields := tool.Input.Fields()
	for _, name := range tool.Descriptor.GetRequiredFields() {
		field := fields.ByName(protoreflect.Name(name))
		if field == nil {
			field = fields.ByJSONName(name)
		}
		if field == nil || !message.Has(field) {
			return fmt.Errorf("required argument %s is missing", name)
		}
	}
	return nil
}

func (r *Registry) Permissions(procedure string) ([]string, bool) {
	tool, ok := r.byProcedure[procedure]
	if !ok {
		return nil, false
	}
	return append([]string(nil), tool.Descriptor.GetPermissions()...), true
}

func (r *Registry) List() ([]*uabv1.ReflectedTool, string) {
	tools := make([]*uabv1.ReflectedTool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, proto.Clone(tool).(*uabv1.ReflectedTool))
	}
	return tools, r.etag
}

func messageSchema(message protoreflect.MessageDescriptor, requiredFields []string, active map[protoreflect.FullName]bool) map[string]any {
	if active[message.FullName()] {
		return map[string]any{"type": "object", "$ref": string(message.FullName())}
	}
	active[message.FullName()] = true
	defer delete(active, message.FullName())
	properties := make(map[string]any)
	fields := message.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		properties[field.JSONName()] = fieldSchema(field, active)
	}
	required := make([]string, 0, len(requiredFields))
	for _, field := range requiredFields {
		descriptor := fields.ByName(protoreflect.Name(field))
		if descriptor == nil {
			descriptor = fields.ByJSONName(field)
		}
		required = append(required, descriptor.JSONName())
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             required,
	}
}

func fieldSchema(field protoreflect.FieldDescriptor, active map[protoreflect.FullName]bool) map[string]any {
	if field.IsMap() {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": scalarSchema(field.MapValue(), active),
		}
	}
	base := scalarSchema(field, active)
	if field.IsList() {
		return map[string]any{"type": "array", "items": base}
	}
	return base
}

func scalarSchema(field protoreflect.FieldDescriptor, active map[protoreflect.FullName]bool) map[string]any {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Uint32Kind,
		protoreflect.Uint64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind, protoreflect.Sfixed32Kind,
		protoreflect.Sfixed64Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.EnumKind:
		values := field.Enum().Values()
		enumeration := make([]string, 0, values.Len())
		for index := 0; index < values.Len(); index++ {
			enumeration = append(enumeration, string(values.Get(index).Name()))
		}
		return map[string]any{"type": "string", "enum": enumeration}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return messageSchema(field.Message(), nil, active)
	default:
		return map[string]any{"type": "string"}
	}
}

func validateRequiredFields(message protoreflect.MessageDescriptor, required []string) error {
	fields := message.Fields()
	for _, name := range required {
		if fields.ByName(protoreflect.Name(name)) == nil && fields.ByJSONName(name) == nil {
			return fmt.Errorf("required field %q does not exist in %s", name, message.FullName())
		}
	}
	return nil
}
