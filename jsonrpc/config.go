package jsonrpc

// Config represents the configuration of the json rpc
type Config struct {
	Host string `mapstructure:"Host"`
	Port int    `mapstructure:"Port"`
}