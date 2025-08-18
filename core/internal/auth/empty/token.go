package empty

type EmptyStorage struct {
	// Type indicates the type (gemini, chatgpt, claude) of token storage.
	Type string `json:"type"`
}

// SaveTokenToFile serializes the token storage to a JSON file.
func (ts *EmptyStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "empty"
	return nil
}
