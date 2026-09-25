import {
  type LlamaCppBackendOptions,
  type MlxBackendOptions,
  type VllmBackendOptions,
  type TabbyBackendOptions,
  LlamaCppBackendOptionsSchema,
  MlxBackendOptionsSchema,
  VllmBackendOptionsSchema,
  TabbyBackendOptionsSchema,
  getAllLlamaCppFieldKeys,
  getAllMlxFieldKeys,
  getAllVllmFieldKeys,
  getAllTabbyFieldKeys,
  getLlamaCppFieldType,
  getMlxFieldType,
  getVllmFieldType,
  getTabbyFieldType
} from '@/schemas/instanceOptions'

// LlamaCpp backend-specific basic fields
const basicLlamaCppFieldsConfig: Record<string, {
  label: string
  description?: string
  placeholder?: string
}> = {
  model: {
    label: 'Model Path',
    placeholder: '/path/to/model.gguf',
    description: 'Path to the model file'
  },
  hf_repo: {
    label: 'Hugging Face Repository',
    placeholder: 'microsoft/DialoGPT-medium',
    description: 'Hugging Face model repository'
  },
  gpu_layers: {
    label: 'GPU Layers',
    placeholder: '0',
    description: 'Number of layers to offload to GPU'
  },
  models_preset: {
    label: 'Models Preset Path',
    placeholder: '/path/to/preset.ini',
    description: 'Optional: Path to preset.ini for router mode. Leave empty for normal operation, or use Preset tab to auto-generate'
  }
}

// MLX backend-specific basic fields
const basicMlxFieldsConfig: Record<string, {
  label: string
  description?: string
  placeholder?: string
}> = {
  model: {
    label: 'Model',
    placeholder: 'mlx-community/Mistral-7B-Instruct-v0.3-4bit',
    description: 'The path to the MLX model weights, tokenizer, and config'
  },
  temp: {
    label: 'Temperature',
    placeholder: '0.0',
    description: 'Default sampling temperature (default: 0.0)'
  },
  top_p: {
    label: 'Top-P',
    placeholder: '1.0',
    description: 'Default nucleus sampling top-p (default: 1.0)'
  },
  top_k: {
    label: 'Top-K',
    placeholder: '0',
    description: 'Default top-k sampling (default: 0, disables top-k)'
  },
  min_p: {
    label: 'Min-P',
    placeholder: '0.0',
    description: 'Default min-p sampling (default: 0.0, disables min-p)'
  },
  max_tokens: {
    label: 'Max Tokens',
    placeholder: '512',
    description: 'Default maximum number of tokens to generate (default: 512)'
  }
}

// vLLM backend-specific basic fields
const basicVllmFieldsConfig: Record<string, {
  label: string
  description?: string
  placeholder?: string
}> = {
  model: {
    label: 'Model',
    placeholder: 'microsoft/DialoGPT-medium',
    description: 'The name or path of the Hugging Face model to use'
  },
  tensor_parallel_size: {
    label: 'Tensor Parallel Size',
    placeholder: '1',
    description: 'Number of GPUs to use for distributed serving'
  },
  gpu_memory_utilization: {
    label: 'GPU Memory Utilization',
    placeholder: '0.9',
    description: 'The fraction of GPU memory to be used for the model executor'
  }
}

// TabbyAPI backend-specific basic fields
const basicTabbyFieldsConfig: Record<string, {
  label: string
  description?: string
  placeholder?: string
}> = {
  model_name: {
    label: 'Model Name',
    placeholder: 'Huihui-Qwen3.8-27B-abliterated-exl3-4.00bpw',
    description: 'Directory name under model_dir (or leave empty to use config.yml)'
  },
  model_dir: {
    label: 'Model Directory',
    placeholder: 'E:\\Model Cache\\tabbyapi\\models',
    description: 'Root directory containing model folders'
  },
  config: {
    label: 'Config Path',
    placeholder: 'E:\\Model Cache\\tabbyapi\\tabbyAPI\\config.yml',
    description: 'Optional absolute path to an overriding config.yml'
  },
  host: {
    label: 'Host',
    placeholder: '127.0.0.1',
    description: 'Bind address (TABBY_NETWORK_HOST / --host)'
  },
  port: {
    label: 'Port',
    placeholder: '8109',
    description: 'Listen port (TABBY_NETWORK_PORT / --port)'
  }
}

// Backend field configuration lookup
const backendFieldConfigs = {
  mlx_lm: basicMlxFieldsConfig,
  vllm: basicVllmFieldsConfig,
  llama_cpp: basicLlamaCppFieldsConfig,
  tabby_api: basicTabbyFieldsConfig,
} as const

const backendFieldGetters = {
  mlx_lm: getAllMlxFieldKeys,
  vllm: getAllVllmFieldKeys,
  llama_cpp: getAllLlamaCppFieldKeys,
  tabby_api: getAllTabbyFieldKeys,
} as const

export function getBasicBackendFields(backendType?: string): string[] {
  const normalizedType = (backendType || 'llama_cpp') as keyof typeof backendFieldConfigs
  const config = backendFieldConfigs[normalizedType] || basicLlamaCppFieldsConfig
  return Object.keys(config)
}

export function getAdvancedBackendFields(backendType?: string): string[] {
  const normalizedType = (backendType || 'llama_cpp') as keyof typeof backendFieldGetters
  const fieldGetter = backendFieldGetters[normalizedType] || getAllLlamaCppFieldKeys
  const basicConfig = backendFieldConfigs[normalizedType] || basicLlamaCppFieldsConfig

  return fieldGetter().filter(key => !(key in basicConfig) && key !== 'extra_args')
}

// Combined backend fields config for use in BackendFormField
export const basicBackendFieldsConfig: Record<string, {
  label: string
  description?: string
  placeholder?: string
}> = {
  ...basicLlamaCppFieldsConfig,
  ...basicMlxFieldsConfig,
  ...basicVllmFieldsConfig,
  ...basicTabbyFieldsConfig
}

// Get field type for any backend option (union type)
export function getBackendFieldType(key: string): 'text' | 'number' | 'boolean' | 'array' {
  // Try to get type from LlamaCpp schema first
  try {
    if (LlamaCppBackendOptionsSchema.shape && key in LlamaCppBackendOptionsSchema.shape) {
      return getLlamaCppFieldType(key as keyof LlamaCppBackendOptions)
    }
  } catch {
    // Schema might not be available
  }

  // Try MLX schema
  try {
    if (MlxBackendOptionsSchema.shape && key in MlxBackendOptionsSchema.shape) {
      return getMlxFieldType(key as keyof MlxBackendOptions)
    }
  } catch {
    // Schema might not be available
  }

  // Try vLLM schema
  try {
    if (VllmBackendOptionsSchema.shape && key in VllmBackendOptionsSchema.shape) {
      return getVllmFieldType(key as keyof VllmBackendOptions)
    }
  } catch {
    // Schema might not be available
  }

  // Try TabbyAPI schema
  try {
    if (TabbyBackendOptionsSchema.shape && key in TabbyBackendOptionsSchema.shape) {
      return getTabbyFieldType(key as keyof TabbyBackendOptions)
    }
  } catch {
    // Schema might not be available
  }

  // Default fallback
  return 'text'
}

