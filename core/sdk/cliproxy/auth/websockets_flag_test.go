package auth

import "testing"

func TestAuthWebsocketsEnabled(t *testing.T) {
	tests := []struct {
		name string
		auth *Auth
		want bool
	}{
		{name: "nil auth", auth: nil, want: false},
		{name: "no attributes or metadata", auth: &Auth{}, want: false},
		{name: "attribute true", auth: &Auth{Attributes: map[string]string{"websockets": "true"}}, want: true},
		{name: "attribute false", auth: &Auth{Attributes: map[string]string{"websockets": "false"}}, want: false},
		{name: "attribute is trimmed", auth: &Auth{Attributes: map[string]string{"websockets": " true "}}, want: true},
		{
			name: "attribute false wins over metadata true",
			auth: &Auth{
				Attributes: map[string]string{"websockets": "false"},
				Metadata:   map[string]any{"websockets": true},
			},
			want: false,
		},
		{
			name: "attribute true wins over metadata false",
			auth: &Auth{
				Attributes: map[string]string{"websockets": "true"},
				Metadata:   map[string]any{"websockets": false},
			},
			want: true,
		},
		{
			name: "empty attribute falls through to metadata",
			auth: &Auth{
				Attributes: map[string]string{"websockets": "  "},
				Metadata:   map[string]any{"websockets": true},
			},
			want: true,
		},
		{
			name: "invalid attribute falls through to metadata",
			auth: &Auth{
				Attributes: map[string]string{"websockets": "yes"},
				Metadata:   map[string]any{"websockets": true},
			},
			want: true,
		},
		{name: "metadata bool true", auth: &Auth{Metadata: map[string]any{"websockets": true}}, want: true},
		{name: "metadata bool false", auth: &Auth{Metadata: map[string]any{"websockets": false}}, want: false},
		{name: "metadata string true", auth: &Auth{Metadata: map[string]any{"websockets": "true"}}, want: true},
		{name: "metadata string is trimmed", auth: &Auth{Metadata: map[string]any{"websockets": " true "}}, want: true},
		{name: "metadata string 1", auth: &Auth{Metadata: map[string]any{"websockets": "1"}}, want: true},
		{name: "metadata string false", auth: &Auth{Metadata: map[string]any{"websockets": "false"}}, want: false},
		{name: "metadata invalid string", auth: &Auth{Metadata: map[string]any{"websockets": "yes"}}, want: false},
		{name: "metadata number is not a flag", auth: &Auth{Metadata: map[string]any{"websockets": 1}}, want: false},
		{name: "metadata nil value", auth: &Auth{Metadata: map[string]any{"websockets": nil}}, want: false},
		{name: "metadata without the key", auth: &Auth{Metadata: map[string]any{"other": true}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authWebsocketsEnabled(tt.auth); got != tt.want {
				t.Fatalf("authWebsocketsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
