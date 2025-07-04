package auth

// TokenStorage defines the structure for storing OAuth2 token information,
// along with associated user and project details. This data is typically
// serialized to a JSON file for persistence.
type TokenStorage struct {
	// Token holds the raw OAuth2 token data, including access and refresh tokens.
	Token any `json:"token"`
	// ProjectID is the Google Cloud Project ID associated with this token.
	ProjectID string `json:"project_id"`
	// Email is the email address of the authenticated user.
	Email string `json:"email"`
	// Auto indicates if the project ID was automatically selected.
	Auto bool `json:"auto"`
	// Checked indicates if the associated Cloud AI API has been verified as enabled.
	Checked bool `json:"checked"`
}
