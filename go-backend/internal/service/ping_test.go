package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

func TestPing(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?/Ping/0/", nil)

	if !Ping(recorder, request, &web.Route{}) {
		t.Fatal("Ping must report the request as handled")
	}

	if http.StatusOK != recorder.Code {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
	if "Pong" != recorder.Body.String() {
		t.Errorf("body = %q, want Pong", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); "text/plain; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}
	// ServicePing calls ServerNoCache() before writing the body.
	if got := recorder.Header().Get("Cache-Control"); "no-store" != got {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestPingHeadHasNoBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodHead, "/?/Ping/0/", nil)

	Ping(recorder, request, &web.Route{})

	if "" != recorder.Body.String() {
		t.Errorf("HEAD body = %q, want nothing", recorder.Body.String())
	}
}

func TestRegisterAddsPingToTheRouter(t *testing.T) {
	paths := httpx.WebPaths{Version: "0.0.0", VersionRoot: t.TempDir()}
	router := web.NewRouter(web.DefaultConfig(), nil, paths, nil)
	Register(router, nil)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?/Ping/0/", nil))

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if "Pong" != recorder.Body.String() {
		t.Errorf("body = %q, want Pong", recorder.Body.String())
	}
}
