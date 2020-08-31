package proto_to_insomnia

// This config is parsed from the input of the insomniaenv_opt command line argument
// It is used to add additional environments besides localhost into the exported Environment
type Config struct {
	Environments map[string]string `json:"environments"`
}
