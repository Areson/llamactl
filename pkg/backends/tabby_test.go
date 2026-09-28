package backends_test

import (
	"llamactl/pkg/backends"
	"llamactl/pkg/config"
	"llamactl/pkg/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		Port:      0,
		Host:      "",
		ModelName: "",
		ModelDir:  "",
		Config:    "",
	}

	args := options.BuildCommandArgs()

	excludedArgs := []string{
		"--port", "0",
		"--host", "",
		"--model-name", "",
		"--model-dir", "",
		"--config", "",
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

	expected := []string{
		"--host", "127.0.0.1",
		"--port", "8109",
		"--model-name", "my-model",
		"--model-dir", `E:\models`,
		"--config", `E:\tabby\config.yml`,
	}

	for _, expectedArg := range expected {
		if !testutil.Contains(args, expectedArg) {
			t.Errorf("Expected argument %q not found in %v", expectedArg, args)
		}
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
			name:      "extra args with known fields",
			command:   "main.py --host 127.0.0.1 --port 8109 --cache-size 262144 --warmup",
			expectErr: false,
			validate: func(t *testing.T, opts *backends.TabbyServerOptions) {
				if opts.Host != "127.0.0.1" {
					t.Errorf("expected host '127.0.0.1', got '%s'", opts.Host)
				}
				if opts.Port != 8109 {
					t.Errorf("expected port 8109, got %d", opts.Port)
				}
				if opts.ExtraArgs == nil {
					t.Fatal("expected extra_args to be non-nil")
				}
				if val, ok := opts.ExtraArgs["cache_size"]; !ok || val != "262144" {
					t.Errorf("expected extra_args[cache_size]='262144', got '%s'", val)
				}
				if val, ok := opts.ExtraArgs["warmup"]; !ok || val != "true" {
					t.Errorf("expected extra_args[warmup]='true', got '%s'", val)
				}
			},
		},
		{
			name:      "only extra args",
			command:   "main.py --max-batch-size 2 --vision",
			expectErr: false,
			validate: func(t *testing.T, opts *backends.TabbyServerOptions) {
				if opts.ExtraArgs == nil {
					t.Fatal("expected extra_args to be non-nil")
				}
				if val, ok := opts.ExtraArgs["max_batch_size"]; !ok || val != "2" {
					t.Errorf("expected extra_args[max_batch_size]='2', got '%s'", val)
				}
				if val, ok := opts.ExtraArgs["vision"]; !ok || val != "true" {
					t.Errorf("expected extra_args[vision]='true', got '%s'", val)
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
		name     string
		config   string
		args     []string
		want     string
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
			name:      "relative config ignored, falls through to main.py",
			config:    "config.yml",
			args:      []string{`E:\tabby\main.py`},
			want:      filepath.Clean(`E:\tabby`),
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
