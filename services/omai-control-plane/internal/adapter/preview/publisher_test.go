package preview

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublisherUsesCapabilityRouteAndStripsCredentials(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Error("publisher forwarded browser credentials to preview code")
		}
		writer.Header().Set("Set-Cookie", "stolen=true")
		_, _ = io.WriteString(writer, "preview:"+request.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	publisher, err := NewPublisher(PublisherConfig{PublicBaseURL: "http://omai.test"})
	if err != nil {
		t.Fatal(err)
	}
	publicURL, err := publisher.Publish(context.Background(), "tenant/workspace", upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(publicURL, "http://omai.test") + "assets/app.js"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Cookie", "control-plane=secret")
	recorder := httptest.NewRecorder()
	publisher.Wrap(http.NotFoundHandler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "preview:/assets/app.js" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Set-Cookie") != "" {
		t.Fatal("publisher forwarded preview Set-Cookie")
	}
	if err := publisher.Unpublish(context.Background(), "tenant/workspace"); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	publisher.Wrap(http.NotFoundHandler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unpublished route status = %d", recorder.Code)
	}
}

func TestPublisherRejectsUnsafeURLsAndUnknownTemplateHosts(t *testing.T) {
	t.Parallel()
	for _, config := range []PublisherConfig{
		{PublicBaseURL: "file:///tmp/preview"},
		{PublicBaseURL: "http://user:password@omai.test"},
		{PublicBaseURL: "https://omai.test", PublicURLTemplate: "https://preview.omai.test/{id}"},
	} {
		if _, err := NewPublisher(config); err == nil {
			t.Fatalf("NewPublisher(%+v) accepted unsafe config", config)
		}
	}
}
