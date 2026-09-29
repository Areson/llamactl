import { z } from 'zod'

// Define the TabbyAPI backend options schema (thin, MLX-sized)
export const TabbyBackendOptionsSchema = z.object({
  // Basic connection options
  host: z.string().optional(),
  port: z.number().optional(),

  // Model selection (Tabby ModelConfig)
  model_name: z.string().optional(),
  model_dir: z.string().optional(),

  // Optional overriding config.yml path
  config: z.string().optional(),

  // Advanced ModelConfig / DraftModelConfig knobs
  cache_size: z.number().optional(),
  cache_mode: z.string().optional(),
  max_batch_size: z.number().optional(),
  draft_mode: z.enum(['model', 'disabled', 'mtp', 'ngram']).optional(),
  draft_num_tokens: z.number().optional(),

  // Vision / multimodal (same model folder; no separate vision path)
  vision: z.boolean().optional(),
  vision_offload: z.boolean().optional(),
  sysmem_multimodal_cache: z.number().optional(),

  // Extra args
  extra_args: z.record(z.string(), z.string()).optional(),
})

// Infer the TypeScript type from the schema
export type TabbyBackendOptions = z.infer<typeof TabbyBackendOptionsSchema>

// Helper to get all TabbyAPI backend option field keys
export function getAllTabbyFieldKeys(): (keyof TabbyBackendOptions)[] {
  return Object.keys(TabbyBackendOptionsSchema.shape) as (keyof TabbyBackendOptions)[]
}

// Get field type for TabbyAPI backend options
export function getTabbyFieldType(key: keyof TabbyBackendOptions): 'text' | 'number' | 'boolean' | 'array' {
  const fieldSchema = TabbyBackendOptionsSchema.shape[key]
  if (!fieldSchema) return 'text'

  const innerSchema = fieldSchema instanceof z.ZodOptional ? fieldSchema.unwrap() : fieldSchema

  if (innerSchema instanceof z.ZodBoolean) return 'boolean'
  if (innerSchema instanceof z.ZodNumber) return 'number'
  if (innerSchema instanceof z.ZodArray) return 'array'
  if (innerSchema instanceof z.ZodEnum) return 'text'
  return 'text'
}
