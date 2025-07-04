package auth

type TokenStorage struct {
	Token     any    `json:"token"`
	ProjectID string `json:"project_id"`
	Email     string `json:"email"`
	Auto      bool   `json:"auto"`
	Checked   bool   `json:"checked"`
}
