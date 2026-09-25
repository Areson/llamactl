package backends

import (
	"encoding/json"
	"fmt"
	"llamactl/pkg/validation"
)

// TabbyServerOptions are the thin, first-class options for the TabbyAPI backend.
// Tabby is primarily config.yml / TABBY_* env driven; these fields map to the
// flat CLI flags Tabby generates from its Pydantic config models (see
// common/args.py). Everything else lands in ExtraArgs.
type TabbyServerOptions struct {
	// Basic connection options (NetworkConfig)
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`

	// Model selection (ModelConfig). ModelName is the directory name under ModelDir;
	// when unset Tabby loads whatever config.yml (or --config) specifies.
	ModelName string `json:"model_name,omitempty"`
	ModelDir  string `json:"model_dir,omitempty"`

	// Config is an optional path to an overriding config.yml (--config).
	// Tabby still loads CWD-relative config.yml unless this is set; prefer an
	// absolute path when the process cwd is not the Tabby install root.
	Config string `json:"config,omitempty"`

	// ExtraArgs are additional command line arguments.
	// Example: {"cache_size": "262144", "warmup": ""}
	ExtraArgs map[string]string `json:"extra_args,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling to collect unknown fields into ExtraArgs
func (o *TabbyServerOptions) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	type tempOptions TabbyServerOptions
	temp := tempOptions{}

	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	*o = TabbyServerOptions(temp)

	knownFields := getKnownFieldNames(o)

	if o.ExtraArgs == nil {
		o.ExtraArgs = make(map[string]string)
	}
	for key, value := range raw {
		if !knownFields[key] {
			o.ExtraArgs[key] = fmt.Sprintf("%v", value)
		}
	}

	return nil
}

func (o *TabbyServerOptions) GetModel() string {
	return o.ModelName
}

func (o *TabbyServerOptions) GetPort() int {
	return o.Port
}

func (o *TabbyServerOptions) SetPort(port int) {
	o.Port = port
}

func (o *TabbyServerOptions) GetHost() string {
	return o.Host
}

func (o *TabbyServerOptions) Validate() error {
	if o == nil {
		return validation.ValidationError(fmt.Errorf("TabbyAPI server options cannot be nil for TabbyAPI backend"))
	}

	if o.Port < 0 || o.Port > 65535 {
		return validation.ValidationError(fmt.Errorf("invalid port range: %d", o.Port))
	}

	return nil
}

// BuildCommandArgs converts to command line arguments
func (o *TabbyServerOptions) BuildCommandArgs() []string {
	multipleFlags := map[string]struct{}{}
	args := BuildCommandArgs(o, multipleFlags)
	args = append(args, convertExtraArgsToFlags(o.ExtraArgs)...)
	return args
}

func (o *TabbyServerOptions) BuildDockerArgs() []string {
	return []string{}
}

// ParseCommand parses a TabbyAPI command string into TabbyServerOptions.
// Supports multiple formats:
// 1. Full command: "python main.py --host 127.0.0.1 --port 8109"
// 2. Script only: "main.py --model-name my-model --port 8109"
// 3. Args only: "--host 0.0.0.0 --port 8109 --config /path/config.yml"
// 4. Multiline commands with backslashes
func (o *TabbyServerOptions) ParseCommand(command string) (any, error) {
	// Treat `python main.py` as executable + subcommand so main.py is not
	// captured as a positional model name by the shared parser.
	executableNames := []string{"main.py", "start.py", "python", "python3", "python.exe"}
	subcommandNames := []string{"main.py", "start.py"}
	multiValuedFlags := map[string]struct{}{}

	var tabbyOptions TabbyServerOptions
	if err := parseCommand(command, executableNames, subcommandNames, multiValuedFlags, &tabbyOptions); err != nil {
		return nil, err
	}

	return &tabbyOptions, nil
}
