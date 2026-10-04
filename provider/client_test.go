package provider

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-plugin-tmdb/metadata"
)

const testAPIKey = "secret-test-key"

func newKeyedTestClient(baseURL string) *Client {
	client := NewClient(1000)
	client.apiKey = testAPIKey
	client.SetBaseURL(baseURL)
	return client
}

func assertNoAPIKey(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	for _, secret := range []string{testAPIKey, defaultAPIKey, "api_key"} {
		if strings.Contains(message, secret) {
			t.Fatalf("error carries %q: %v", secret, err)
		}
	}
}

// closedServerURL returns the URL of a server that no longer listens, so a
// request to it fails in transport.
func closedServerURL(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	baseURL := server.URL
	server.Close()
	return baseURL
}

func TestTransportErrorsLeaveOutTheAPIKey(t *testing.T) {
	t.Parallel()

	baseURL := closedServerURL(t)
	p := NewProviderWithClient(newKeyedTestClient(baseURL))

	_, err := p.Search(context.Background(), metadata.SearchQuery{Title: "Movie", ContentType: "movie"})
	assertNoAPIKey(t, err)
	if !strings.Contains(err.Error(), "tmdb: load config: tmdb: request failed:") {
		t.Fatalf("error lost its context: %v", err)
	}

	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("error %v does not wrap a *url.Error", err)
	}
	if want := baseURL + "/configuration"; urlErr.URL != want {
		t.Fatalf("error URL = %q, want %q", urlErr.URL, want)
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("error %v no longer wraps the transport *net.OpError", err)
	}
}

func TestCanceledRequestErrorKeepsItsCause(t *testing.T) {
	t.Parallel()

	arrived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	}))
	defer server.Close()

	client := newKeyedTestClient(server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()

	err := client.loadConfiguration(ctx)
	assertNoAPIKey(t, err)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRedirectTransportErrorLeavesOutTheAPIKey(t *testing.T) {
	t.Parallel()

	target := closedServerURL(t) + "/configuration?api_key=" + testAPIKey
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer server.Close()

	err := newKeyedTestClient(server.URL).loadConfiguration(context.Background())
	assertNoAPIKey(t, err)
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("error %v no longer wraps the transport *net.OpError", err)
	}
}

func TestCreateRequestErrorLeavesOutTheAPIKey(t *testing.T) {
	t.Parallel()

	// An unclosed IPv6 literal makes url.Parse fail inside
	// http.NewRequestWithContext, which echoes the URL it rejected.
	err := newKeyedTestClient("http://[::1").loadConfiguration(context.Background())
	assertNoAPIKey(t, err)
	if !strings.Contains(err.Error(), "tmdb: create request:") {
		t.Fatalf("error lost its context: %v", err)
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("error %v does not wrap a *url.Error", err)
	}
}

func TestStatusErrorLeavesOutTheAPIKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status_code":7,"status_message":"Invalid API key: You must be granted a valid key."}`))
	}))
	defer server.Close()

	err := newKeyedTestClient(server.URL).loadConfiguration(context.Background())
	assertNoAPIKey(t, err)
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("error lost its status: %v", err)
	}
}

func TestRedactURLErrorRedactsNestedURLErrors(t *testing.T) {
	t.Parallel()

	cause := errors.New("connection refused")
	err := redactURLError(&url.Error{
		Op:  "Get",
		URL: "https://api.themoviedb.org/3/configuration?api_key=" + testAPIKey,
		Err: &url.Error{
			Op:  "Get",
			URL: "https://redirect.example/3/configuration?api_key=" + testAPIKey + "#fragment",
			Err: cause,
		},
	})

	assertNoAPIKey(t, err)
	want := `Get "https://api.themoviedb.org/3/configuration": Get "https://redirect.example/3/configuration": connection refused`
	if err.Error() != want {
		t.Fatalf("redacted error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("redacted error %v lost its cause", err)
	}
}

func TestRedactURLErrorPassesOtherErrorsThrough(t *testing.T) {
	t.Parallel()

	if err := redactURLError(nil); err != nil {
		t.Fatalf("redactURLError(nil) = %v, want nil", err)
	}
	if err := redactURLError(context.DeadlineExceeded); err != context.DeadlineExceeded { //nolint:errorlint // identity is the point
		t.Fatalf("redactURLError changed a non-URL error: %v", err)
	}
}
