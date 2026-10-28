package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAdminAuthMiddlewareRequiresConfiguredBearerKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := &Server{AdminAPIKey: "admin-secret"}
	router := gin.New()
	router.Use(server.AdminAuthMiddleware())
	router.GET("/admin", func(c *gin.Context) { c.Status(http.StatusOK) })

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "raw key", header: "admin-secret", want: http.StatusUnauthorized},
		{name: "user token", header: "Bearer sk-user-token", want: http.StatusUnauthorized},
		{name: "wrong admin key", header: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "valid admin key", header: "Bearer admin-secret", want: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tt.want {
				t.Fatalf("status = %d; want %d", response.Code, tt.want)
			}
			if strings.Contains(response.Body.String(), server.AdminAPIKey) {
				t.Fatalf("response contains the admin API key")
			}
		})
	}
}

func TestRegisterAdminRoutesRejectUnauthorizedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := &Server{AdminAPIKey: "admin-secret"}
	router := gin.New()
	server.Register(router)

	endpoints := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/admin/users/create"},
		{method: http.MethodPost, path: "/admin/tokens/create"},
		{method: http.MethodDelete, path: "/admin/users/1"},
		{method: http.MethodDelete, path: "/admin/tokens/1"},
	}
	authHeaders := []string{"", "Bearer wrong-admin", "Bearer sk-user-token"}

	for _, endpoint := range endpoints {
		for _, header := range authHeaders {
			t.Run(endpoint.method+endpoint.path+"/"+header, func(t *testing.T) {
				req := httptest.NewRequest(endpoint.method, endpoint.path, nil)
				if header != "" {
					req.Header.Set("Authorization", header)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d; want %d", response.Code, http.StatusUnauthorized)
				}
			})
		}
	}
}
