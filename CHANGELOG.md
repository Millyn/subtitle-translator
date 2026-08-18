# Changelog

## 0.5.0 (2026-08-18)

### Bug Fixes

- **API Timeout**: Default timeout increased from 5s to 60s, with separated connection/TLS/response timeouts and exponential backoff retry

### New Features

- **Debug Toggle**: Dynamic enable/disable debug mode from `/debug` page without restart
- **Prompt Management**: New `/prompt` page for viewing and editing system prompt and background prompt
- **Current Model Indicator**: `/models` page now highlights the currently active ASR model
- **Remote Model Discovery**: `/models` page can fetch latest models from sherpa-onnx GitHub releases

### Improvements

- **Token Optimization**: System prompt restructured for better prompt caching, reducing token consumption
- **Exponential Backoff**: API retry uses exponential backoff (500ms→1s→2s→4s) instead of linear delay

## 0.4.0 (2026-08-17)

### Improvements

- **Reduced Token Usage**: Only matched glossary terms are sent to API instead of the entire glossary, significantly reducing token consumption
- **Debug Panel Redesign**: New split-pane layout with event list on left and detailed view on right for better readability
- **Subtitle Style Editor**: New `/editor` page for customizing subtitle appearance with real-time preview

## 0.3.0 (2026-08-17)

### New Features

- **Model Management Page** (`/models`): Interactive UI for browsing, downloading, and deleting ASR models from sherpa-onnx official releases
- **Unified Dashboard** (`/dashboard`): Single entry point for all management pages with access level indicators
- **New ASR Models**: Added Cohere Transcribe 14-language INT8, Qwen3-ASR 0.6B INT8, NeMo Parakeet TDT v3 INT8 (total 7 models)
- **Startup Memory**: Program now remembers last used microphone and model, reducing manual selection on restart
- **DEBUG API Inspection**: Request/response bodies from DeepSeek API now visible in debug-ws panel when debug mode is enabled

### Bug Fixes

- **Glossary Duplicate Tolerance**: Duplicate profile names in glossaries folder no longer crash the program; auto-deduplication enabled by default via `auto_deduplicate_glossary` config option

### Improvements

- API Key is masked in debug output (shows only first/last 4 characters)
- Model download uses SSE for real-time progress tracking
- Dashboard shows access permissions (public/local-only) for each page

### Configuration

New config options in `config.json`:
- `translation.auto_deduplicate_glossary` (bool, default: true) - Auto-deduplicate glossary profiles
- `audio.last_device_index` (int) - Remembered microphone device
- `asr.last_model_id` (string) - Remembered ASR model

## 0.2.0

- Initial stable release
