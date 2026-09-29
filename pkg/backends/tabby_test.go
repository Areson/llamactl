package backends_test

import (
	"llamactl/pkg/backends"
	"llamactl/pkg/config"
	"llamactl/pkg/testutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseTabbyCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		expectErr bool
		validate  func(*testing.T, *backends.TabbyServerOptions)
	}{
		{
			name:      "basic command",
			command:   "python main.py --host 127.0.0.1 --port 8109",
			expectErr: false,
			validate: func(t *testing.T, opts *backends.TabbyServerOptions) {
				if opts.Host != "127.0.0.1" {
					t.Errorf("expected host '127.0.0.1', got '%s'", opts.Host)
				}
				if opts.Port != 8109 {
					t.Errorf("expected port 8109, got %d", opts.Port)
				}
			},
		},
		{
			name:      "args only with model",
			command:   "--model-name my-model --model-dir /models --port 8080",
			expectErr: false,
			validate: func(t *testing.T, opts *backends.TabbyServerOptions) {
				if opts.ModelName != "my-model" {
					t.Errorf("expected model_name 'my-model', got '%s'", opts.ModelName)
				}
				if opts.ModelDir != "/models" {
					t.Errorf("expected model_dir '/models', got '%s'", opts.ModelDir)
				}
				if opts.Port != 8080 {
					t.Errorf("expected port 8080, got %d", opts.Port)
				}
			},
		},
		{
			name:      "config override",
			command:   "main.py --config /path/to/config.yml --host 0.0.0.0",
			expectErr: false,
			validate: func(t *testing.T, opts *backends.TabbyServerOptions) {
				if opts.Config != "/path/to/config.yml" {
					t.Errorf("expected config '/path/to/config.yml', got '%s'", opts.Config)
				}
				if opts.Host != "0.0.0.0" {
					t.Errorf("expected host '0.0.0.0', got '%s'", opts.Host)
				}
			},
		},
		{
			name:      "equals form",
			command:   "python.exe --model-name=qwen --port=8115",
			expectErr: false,
			validate: func(t *testing.T, opts *backends.TabbyServerOptions) {
				if opts.ModelName != "qwen" {
					t.Errorf("expected model_name 'qwen', got '%s'", opts.ModelName)
				}
				if opts.Port != 8115 {
					t.Errorf("expected port 8115, got %d", opts.Port)
				}
			},
		},
		{
			name:      "empty command",
			command:   "",
			expectErr: true,
		},
		{
			name:      "unterminated quote",
			command:   `main.py --model-name "unterminated`,
			expectErr: true,
		},
		{
			name:      "malformed flag",
			command:   "main.py ---host 127.0.0.1",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts backends.TabbyServerOptions
			resultAny, err := opts.ParseCommand(tt.command)
			result, _ := resultAny.(*backends.TabbyServerOptions)

			if tt.expectErr {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result == nil {
				t.Errorf("expected result but got nil")
				return
			}

			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestTabbyValidate(t *testing.T) {
	t.Run("nil options", func(t *testing.T) {
		var opts *backends.TabbyServerOptions
		if err := opts.Validate(); err == nil {
			t.Error("expected error for nil options")
		}
	})

	t.Run("invalid port", func(t *testing.T) {
		opts := &backends.TabbyServerOptions{Port: 70000}
		if err := opts.Validate(); err == nil {
			t.Error("expected error for invalid port")
		}
	})

	t.Run("valid options", func(t *testing.T) {
		opts := &backends.TabbyServerOptions{Host: "127.0.0.1", Port: 8109}
		if err := opts.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestTabbyBuildCommandArgs_ZeroValues(t *testing.T) {
	options := backends.TabbyServerOptions{
		Port:                  0,
		Host:                  "",
		ModelName:             "",
		ModelDir:              "",
		Config:                "",
		CacheSize:             0,
		CacheMode:             "",
		MaxBatchSize:          0,
		DraftMode:             "",
		DraftNumTokens:        0,
		Vision:                false,
		VisionOffload:         false,
		SysmemMultimodalCache: 0,
	}

	args := options.BuildCommandArgs()

	excludedArgs := []string{
		"--port", "0",
		"--host", "",
		"--model-name", "",
		"--model-dir", "",
		"--config", "",
		"--cache-size", "0",
		"--cache-mode", "",
		"--max-batch-size", "0",
		"--draft-mode", "",
		"--draft-num-tokens", "0",
		"--vision",
		"--vision-offload",
		"--sysmem-multimodal-cache", "0",
	}

	for _, excludedArg := range excludedArgs {
		if testutil.Contains(args, excludedArg) {
			t.Errorf("Zero value argument %q should not be present in %v", excludedArg, args)
		}
	}
}

func TestTabbyBuildCommandArgs_Fields(t *testing.T) {
	options := backends.TabbyServerOptions{
		Host:      "127.0.0.1",
		Port:      8109,
		ModelName: "my-model",
		ModelDir:  `E:\models`,
		Config:    `E:\tabby\config.yml`,
	}

	args := options.BuildCommandArgs()

	// Port + config.yml: --config is omitted so CLI --port is honored
	// (Tabby _from_args early-returns on --config and drops --port).
	expected := []string{
		"--host", "127.0.0.1",
		"--port", "8109",
		"--model-name", "my-model",
		"--model-dir", `E:\models`,
	}

	for _, expectedArg := range expected {
		if !testutil.Contains(args, expectedArg) {
			t.Errorf("Expected argument %q not found in %v", expectedArg, args)
		}
	}
	if testutil.Contains(args, "--config") {
		t.Errorf("--config must be omitted when Config basename is config.yml and Port is set; got %v", args)
	}
	// Stored options must not be mutated.
	if options.Config != `E:\tabby\config.yml` {
		t.Errorf("BuildCommandArgs mutated Config: got %q", options.Config)
	}
}

func TestTabbyBuildCommandArgs_AdvancedFields(t *testing.T) {
	options := backends.TabbyServerOptions{
		Host:           "127.0.0.1",
		Port:           8116,
		CacheSize:      262144,
		CacheMode:      "Q4",
		MaxBatchSize:   2,
		DraftMode:      "mtp",
		DraftNumTokens: 5,
		ExtraArgs: map[string]string{
			"warmup": "",
		},
	}

	args := options.BuildCommandArgs()

	expected := []string{
		"--host", "127.0.0.1",
		"--port", "8116",
		"--cache-size", "262144",
		"--cache-mode", "Q4",
		"--max-batch-size", "2",
		"--draft-mode", "mtp",
		"--draft-num-tokens", "5",
		"--warmup", "true",
	}

	for _, expectedArg := range expected {
		if !testutil.Contains(args, expectedArg) {
			t.Errorf("Expected argument %q not found in %v", expectedArg, args)
		}
	}
	if !hasFlagValue(args, "--warmup", "true") {
		t.Errorf("expected --warmup true (Tabby typed bool), got %v", args)
	}
}

func TestTabbyBuildCommandArgs_VisionFields(t *testing.T) {
	options := backends.TabbyServerOptions{
		Host:                  "127.0.0.1",
		Port:                  8117,
		Vision:                true,
		VisionOffload:         true,
		SysmemMultimodalCache: 2048,
		ExtraArgs: map[string]string{
			"warmup": "",
		},
	}

	args := options.BuildCommandArgs()

	// Tabby argparse (common/args.py) is not store_true for bools — emit
	// "--vision true" / "--vision-offload true" / "--warmup true".
	expected := []string{
		"--host", "127.0.0.1",
		"--port", "8117",
		"--vision", "true",
		"--vision-offload", "true",
		"--sysmem-multimodal-cache", "2048",
		"--warmup", "true",
	}

	for _, expectedArg := range expected {
		if !testutil.Contains(args, expectedArg) {
			t.Errorf("Expected argument %q not found in %v", expectedArg, args)
		}
	}
	for _, pair := range [][2]string{
		{"--vision", "true"},
		{"--vision-offload", "true"},
		{"--warmup", "true"},
	} {
		if !hasFlagValue(args, pair[0], pair[1]) {
			t.Errorf("expected %s %s (Tabby typed bool), got %v", pair[0], pair[1], args)
		}
	}
}

// hasFlagValue reports whether args contains consecutive flag, value.
func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestTabbyOmitConfigForPort(t *testing.T) {
	tests := []struct {
		name   string
		config string
		port   int
		want   bool
	}{
		{name: "config.yml with port", config: `E:\tabby\config.yml`, port: 8116, want: true},
		{name: "CONFIG.YML case insensitive", config: `E:\tabby\CONFIG.YML`, port: 8116, want: true},
		{name: "custom name keeps config", config: `E:\tabby\custom.yml`, port: 8116, want: false},
		{name: "no port", config: `E:\tabby\config.yml`, port: 0, want: false},
		{name: "empty config", config: "", port: 8116, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backends.TabbyOmitConfigForPort(tt.config, tt.port)
			if got != tt.want {
				t.Fatalf("TabbyOmitConfigForPort(%q, %d) = %v, want %v", tt.config, tt.port, got, tt.want)
			}
		})
	}
}

func TestTabbyBuildCommandArgs_OmitsConfigYmlWhenPortSet(t *testing.T) {
	opts := backends.TabbyServerOptions{
		Port:   8116,
		Config: `E:\Model Cache\tabbyapi\tabbyAPI\config.yml`,
		Host:   "127.0.0.1",
	}
	args := opts.BuildCommandArgs()

	if !testutil.Contains(args, "--port") || !testutil.Contains(args, "8116") {
		t.Fatalf("expected --port 8116 in %v", args)
	}
	if testutil.Contains(args, "--config") {
		t.Fatalf("expected --config omitted for config.yml + port, got %v", args)
	}
	if opts.Config == "" || opts.Port != 8116 {
		t.Fatalf("options mutated: config=%q port=%d", opts.Config, opts.Port)
	}
}

func TestTabbyBuildCommandArgs_CustomConfigRewritesPort(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "custom-config.yml")
	srcYAML := []byte("network:\n  host: 127.0.0.1\n  port: 8109\nmodel:\n  model_name: demo\n")
	if err := os.WriteFile(src, srcYAML, 0o644); err != nil {
		t.Fatal(err)
	}

	opts := backends.TabbyServerOptions{
		Port:   8116,
		Config: src,
		Host:   "127.0.0.1",
	}
	args := opts.BuildCommandArgs()

	// Must still pass --config (custom name), but pointing at a temp file
	// whose network.port is 8116 — not the original 8109.
	cfgIdx := -1
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			cfgIdx = i + 1
			break
		}
	}
	if cfgIdx < 0 {
		t.Fatalf("expected --config <temp> in %v", args)
	}
	tempPath := args[cfgIdx]
	if tempPath == src {
		t.Fatalf("--config still points at source %q; expected temp rewrite", src)
	}
	if !testutil.Contains(args, "--port") || !testutil.Contains(args, "8116") {
		t.Fatalf("expected --port 8116 still present in %v", args)
	}

	data, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatalf("read temp config: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse temp config: %v", err)
	}
	network, _ := doc["network"].(map[string]any)
	if network == nil {
		t.Fatalf("temp config missing network: %s", data)
	}
	portVal := network["port"]
	switch p := portVal.(type) {
	case int:
		if p != 8116 {
			t.Fatalf("temp network.port = %d, want 8116", p)
		}
	case int64:
		if p != 8116 {
			t.Fatalf("temp network.port = %d, want 8116", p)
		}
	default:
		// yaml.v3 may decode as int or float64 depending on content
		if fmt := stringifyPort(portVal); fmt != "8116" {
			t.Fatalf("temp network.port = %v (%T), want 8116", portVal, portVal)
		}
	}
	if name, _ := doc["model"].(map[string]any)["model_name"].(string); name != "demo" {
		t.Fatalf("temp config lost model_name: %v", doc["model"])
	}
	// Original file unchanged; stored options unchanged.
	orig, _ := os.ReadFile(src)
	if string(orig) != string(srcYAML) {
		t.Fatal("source config was modified")
	}
	if opts.Config != src {
		t.Fatalf("options.Config mutated to %q", opts.Config)
	}
	_ = os.Remove(tempPath)
}

func stringifyPort(v any) string {
	switch p := v.(type) {
	case int:
		return strconv.Itoa(p)
	case int64:
		return strconv.FormatInt(p, 10)
	case float64:
		return strconv.Itoa(int(p))
	default:
		return ""
	}
}

func TestWriteTabbyConfigWithPort(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cfg.yml")
	if err := os.WriteFile(src, []byte("network:\n  port: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := backends.WriteTabbyConfigWithPort(src, 8116)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	network := doc["network"].(map[string]any)
	if stringifyPort(network["port"]) != "8116" {
		t.Fatalf("port = %v, want 8116", network["port"])
	}
}

func TestParseTabbyCommand_ExtraArgs(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		expectErr bool
		validate  func(*testing.T, *backends.TabbyServerOptions)
	}{
		{
			name:      "typed advanced fields with remaining extra args",
			command:   "main.py --host 127.0.0.1 --port 8109 --cache-size 262144 --warmup",
			expectErr: false,
			validate: func(t *testing.T, opts *backends.TabbyServerOptions) {
				if opts.Host != "127.0.0.1" {
					t.Errorf("expected host '127.0.0.1', got '%s'", opts.Host)
				}
				if opts.Port != 8109 {
					t.Errorf("expected port 8109, got %d", opts.Port)
				}
				if opts.CacheSize != 262144 {
					t.Errorf("expected cache_size 262144, got %d", opts.CacheSize)
				}
				if _, ok := opts.ExtraArgs["cache_size"]; ok {
					t.Error("cache_size should be typed, not in extra_args")
				}
				if opts.ExtraArgs == nil {
					t.Fatal("expected extra_args to be non-nil")
				}
				if val, ok := opts.ExtraArgs["warmup"]; !ok || val != "true" {
					t.Errorf("expected extra_args[warmup]='true', got '%s'", val)
				}
			},
		},
		{
			name:      "typed max_batch_size and vision with remaining extra args",
			command:   "main.py --max-batch-size 2 --vision --vision-offload --sysmem-multimodal-cache 2048 --warmup",
			expectErr: false,
			validate: func(t *testing.T, opts *backends.TabbyServerOptions) {
				if opts.MaxBatchSize != 2 {
					t.Errorf("expected max_batch_size 2, got %d", opts.MaxBatchSize)
				}
				if !opts.Vision {
					t.Error("expected vision true")
				}
				if !opts.VisionOffload {
					t.Error("expected vision_offload true")
				}
				if opts.SysmemMultimodalCache != 2048 {
					t.Errorf("expected sysmem_multimodal_cache 2048, got %d", opts.SysmemMultimodalCache)
				}
				if _, ok := opts.ExtraArgs["max_batch_size"]; ok {
					t.Error("max_batch_size should be typed, not in extra_args")
				}
				if _, ok := opts.ExtraArgs["vision"]; ok {
					t.Error("vision should be typed, not in extra_args")
				}
				if _, ok := opts.ExtraArgs["vision_offload"]; ok {
					t.Error("vision_offload should be typed, not in extra_args")
				}
				if _, ok := opts.ExtraArgs["sysmem_multimodal_cache"]; ok {
					t.Error("sysmem_multimodal_cache should be typed, not in extra_args")
				}
				if opts.ExtraArgs == nil {
					t.Fatal("expected extra_args to be non-nil")
				}
				if val, ok := opts.ExtraArgs["warmup"]; !ok || val != "true" {
					t.Errorf("expected extra_args[warmup]='true', got '%s'", val)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts backends.TabbyServerOptions
			result, err := opts.ParseCommand(tt.command)

			if tt.expectErr && err == nil {
				t.Error("expected error but got none")
				return
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if !tt.expectErr && tt.validate != nil {
				tabbyOpts, ok := result.(*backends.TabbyServerOptions)
				if !ok {
					t.Fatal("result is not *TabbyServerOptions")
				}
				tt.validate(t, tabbyOpts)
			}
		})
	}
}

func TestTabbyGetCommand_NoDocker(t *testing.T) {
	backendConfig := &config.BackendConfig{
		Tabby: config.BackendSettings{
			Command: "python",
			Args:    []string{"main.py"},
			Docker: &config.DockerSettings{
				Enabled: true,
				Image:   "test-image",
			},
		},
	}

	opts := backends.Options{
		BackendType: backends.BackendTypeTabbyAPI,
		TabbyServerOptions: &backends.TabbyServerOptions{
			Host: "127.0.0.1",
			Port: 8109,
		},
	}

	tests := []struct {
		name            string
		dockerEnabled   *bool
		commandOverride string
		expected        string
	}{
		{
			name:            "ignores docker in config",
			dockerEnabled:   nil,
			commandOverride: "",
			expected:        "python",
		},
		{
			name:            "ignores docker override",
			dockerEnabled:   boolPtr(true),
			commandOverride: "",
			expected:        "python",
		},
		{
			name:            "respects command override",
			dockerEnabled:   nil,
			commandOverride: `E:\Model Cache\tabbyapi\venv\Scripts\python.exe`,
			expected:        `E:\Model Cache\tabbyapi\venv\Scripts\python.exe`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := opts.GetCommand(backendConfig, tt.dockerEnabled, tt.commandOverride)
			if result != tt.expected {
				t.Errorf("GetCommand() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestTabbyBuildEnvironment_InjectsSlotsShim(t *testing.T) {
	backendConfig := &config.BackendConfig{
		Tabby: config.BackendSettings{
			Command: "python",
			Args:    []string{"main.py"},
			Environment: map[string]string{
				"TABBYAPI_LLAMACTL_TIMING": "1",
			},
		},
		LlamaCpp: config.BackendSettings{
			Command: "llama-server",
			Environment: map[string]string{
				"LLAMA_ARG": "1",
			},
		},
	}

	tabbyOpts := backends.Options{
		BackendType:        backends.BackendTypeTabbyAPI,
		TabbyServerOptions: &backends.TabbyServerOptions{Port: 8109},
	}
	env := tabbyOpts.BuildEnvironment(backendConfig, nil, nil)
	if env["TABBYAPI_LLAMACTL_TIMING"] != "1" {
		t.Fatalf("expected timing env preserved, got %q", env["TABBYAPI_LLAMACTL_TIMING"])
	}
	pp := env["PYTHONPATH"]
	if pp == "" {
		t.Fatal("expected PYTHONPATH for tabby_api")
	}
	first := strings.Split(pp, string(os.PathListSeparator))[0]
	if _, err := os.Stat(filepath.Join(first, "sitecustomize.py")); err != nil {
		t.Fatalf("sitecustomize.py missing on PYTHONPATH %q: %v", pp, err)
	}

	llamaOpts := backends.Options{
		BackendType:        backends.BackendTypeLlamaCpp,
		LlamaServerOptions: &backends.LlamaServerOptions{Port: 8080},
	}
	llamaEnv := llamaOpts.BuildEnvironment(backendConfig, nil, nil)
	if _, ok := llamaEnv["PYTHONPATH"]; ok {
		t.Fatalf("llama_cpp must not get tabby PYTHONPATH shim, got %q", llamaEnv["PYTHONPATH"])
	}
}

func TestTabbyWorkingDir(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		args      []string
		want      string
		wantEmpty bool
	}{
		{
			name:   "absolute config wins",
			config: `E:\Model Cache\tabbyapi\tabbyAPI\config.yml`,
			args:   []string{`E:\Model Cache\tabbyapi\tabbyAPI\main.py`, "--host", "127.0.0.1"},
			want:   filepath.Clean(`E:\Model Cache\tabbyapi\tabbyAPI`),
		},
		{
			name:   "config preferred over main.py in different dir",
			config: `E:\tabby\install\config.yml`,
			args:   []string{`D:\other\main.py`},
			want:   filepath.Clean(`E:\tabby\install`),
		},
		{
			name:   "absolute main.py when no config",
			config: "",
			args:   []string{`E:/Model Cache/tabbyapi/tabbyAPI/main.py`, "--port", "8116"},
			want:   filepath.Clean(`E:/Model Cache/tabbyapi/tabbyAPI`),
		},
		{
			name:   "absolute start.py when no config",
			config: "",
			args:   []string{"--host", "0.0.0.0", `D:\opt\tabbyAPI\start.py`},
			want:   filepath.Clean(`D:\opt\tabbyAPI`),
		},
		{
			name:   "relative config ignored, falls through to main.py",
			config: "config.yml",
			args:   []string{`E:\tabby\main.py`},
			want:   filepath.Clean(`E:\tabby`),
		},
		{
			name:      "relative main.py alone yields empty",
			config:    "",
			args:      []string{"main.py", "--port", "8080"},
			wantEmpty: true,
		},
		{
			name:      "no config no main.py yields empty",
			config:    "",
			args:      []string{"--host", "127.0.0.1"},
			wantEmpty: true,
		},
		{
			name:      "empty inputs",
			config:    "",
			args:      nil,
			wantEmpty: true,
		},
		{
			name:      "relative config only yields empty",
			config:    "config.yml",
			args:      nil,
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backends.TabbyWorkingDir(tt.config, tt.args)
			if tt.wantEmpty {
				if got != "" {
					t.Fatalf("TabbyWorkingDir() = %q, want empty", got)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("TabbyWorkingDir() = %q, want %q", got, tt.want)
			}
		})
	}
}
