package backends

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"llamactl/pkg/validation"

	"gopkg.in/yaml.v3"
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
	//
	// NOTE: Tabby's common/tabby_config.py _from_args returns early when
	// --config is set, so CLI --port is ignored and the YAML network.port
	// wins. BuildCommandArgs compensates (see TabbyOmitConfigForPort /
	// WriteTabbyConfigWithPort) so GetPort() always matches the bind port.
	Config string `json:"config,omitempty"`

	// Advanced ModelConfig / DraftModelConfig knobs (first-class, MLX-shaped).
	// Map to Tabby CLI flags via BuildCommandArgs (snake_case -> kebab-case).
	// CUDA_VISIBLE_DEVICES stays an instance environment var, not a field here.
	CacheSize      int    `json:"cache_size,omitempty"`
	CacheMode      string `json:"cache_mode,omitempty"`
	MaxBatchSize   int    `json:"max_batch_size,omitempty"`
	DraftMode      string `json:"draft_mode,omitempty"`
	DraftNumTokens int    `json:"draft_num_tokens,omitempty"`

	// Vision / multimodal (ModelConfig + PerformanceConfig). Tabby loads the
	// vision tower from the same model_name folder when the model supports it;
	// there is no separate vision model path.
	Vision                bool `json:"vision,omitempty"`
	VisionOffload         bool `json:"vision_offload,omitempty"`
	SysmemMultimodalCache int  `json:"sysmem_multimodal_cache,omitempty"`

	// ExtraArgs are additional command line arguments.
	// Example: {"warmup": ""}
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

// TabbyOmitConfigForPort reports whether BuildCommandArgs should drop --config
// so CLI --port is honored. Tabby ignores CLI --port when --config is set
// (_from_args early return). When the override file is named config.yml and
// TabbyWorkingDir has set cwd to that file's directory, Tabby still loads the
// same YAML from CWD; omitting --config lets --port win the merge.
func TabbyOmitConfigForPort(config string, port int) bool {
	if port <= 0 || config == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(config), "config.yml")
}

// WriteTabbyConfigWithPort reads srcConfig, deep-sets network.port, and writes
// a temp YAML. Used when Config is a non-config.yml path so --config must stay
// on the CLI (early return would otherwise discard --port). The returned path
// is the temp file; callers may leave it for process lifetime (OS temp cleanup).
func WriteTabbyConfigWithPort(srcConfig string, port int) (string, error) {
	data, err := os.ReadFile(srcConfig)
	if err != nil {
		return "", fmt.Errorf("read tabby config %q: %w", srcConfig, err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("parse tabby config %q: %w", srcConfig, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}

	network, _ := doc["network"].(map[string]any)
	if network == nil {
		network = map[string]any{}
		doc["network"] = network
	}
	network["port"] = port

	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal tabby config with port: %w", err)
	}

	f, err := os.CreateTemp("", "llamactl-tabby-config-*.yml")
	if err != nil {
		return "", fmt.Errorf("create temp tabby config: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(out); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("write temp tabby config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close temp tabby config: %w", err)
	}
	return path, nil
}

// BuildCommandArgs converts to command line arguments.
//
// Tabby _from_args returns early when --config is set, discarding CLI --port
// so config.yml network.port wins and GetPort() disagrees with the bind port.
// When both Config and Port are set we either omit --config (config.yml + cwd)
// or rewrite Config to a temp YAML with network.port overridden.
func (o *TabbyServerOptions) BuildCommandArgs() []string {
	multipleFlags := map[string]struct{}{}

	// Shallow copy so we can adjust Config for the arg list without mutating
	// the stored options (instance JSON / GetPort must keep the real values).
	cp := *o
	if cp.Port > 0 && cp.Config != "" {
		if TabbyOmitConfigForPort(cp.Config, cp.Port) {
			cp.Config = ""
		} else if path, err := WriteTabbyConfigWithPort(cp.Config, cp.Port); err == nil {
			cp.Config = path
		}
		// On WriteTabbyConfigWithPort error, keep original --config (degraded:
		// port may mismatch). Spawn still works; health will surface the miss.
	}

	args := BuildCommandArgs(&cp, multipleFlags)
	args = append(args, convertExtraArgsToFlags(cp.ExtraArgs)...)
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

// TabbyWorkingDir returns the process working directory for a TabbyAPI spawn so
// relative assets (sampler_overrides/, default config.yml) resolve against the
// install root rather than llamactl's CWD.
//
// Derivation order:
//  1. If config is an absolute path, use its directory.
//  2. Else if args contain an absolute path whose base is main.py or start.py,
//     use that file's directory.
//  3. Else return "" (leave cmd.Dir unset).
func TabbyWorkingDir(config string, args []string) string {
	if dir := absFileDir(config); dir != "" {
		return dir
	}
	for _, a := range args {
		base := filepath.Base(a)
		if base == "main.py" || base == "start.py" {
			if dir := absFileDir(a); dir != "" {
				return dir
			}
		}
	}
	return ""
}

func absFileDir(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(filepath.Dir(path))
}
