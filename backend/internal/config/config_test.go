package config

import "testing"

func TestValidateProviderURLs(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		allowHTTP bool
		wantErr   bool
	}{
		{name: "https accepted", url: "https://provider.example/v1/chat", wantErr: false},
		{name: "http rejected by default", url: "http://provider.example/v1/chat", wantErr: true},
		{name: "http accepted for explicit test mode", url: "http://127.0.0.1:18080/chat", allowHTTP: true, wantErr: false},
		{name: "missing host rejected", url: "https:///chat", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Providers: []Provider{{Name: "test", Url: tt.url}}}
			err := cfg.ValidateProviderURLs(tt.allowHTTP)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateProviderURLs() error = %v; wantErr %v", err, tt.wantErr)
			}
		})
	}
}
