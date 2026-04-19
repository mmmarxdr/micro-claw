# MicroAgent — Test Plan MVP

> Este documento define todos los tests necesarios para validar que el MVP de Daimon cumple con la especificación de `DAIMON.md`. Cada test es concreto, implementable, y mapea directamente a un requisito del Definition of Done o a un contrato de interfaz.

---

## Convenciones

- Todos los tests usan `testing` de stdlib + table-driven tests.
- Los mocks se definen en archivos `_test.go`, sin frameworks externos.
- Cada sección corresponde a un paquete del proyecto.
- Los tests están ordenados por prioridad: los que validan contratos e invariantes de seguridad van primero.

---

## 1. `internal/config` — Configuración

### 1.1 Parsing YAML básico

```go
func TestLoadConfig_ValidYAML(t *testing.T)
```

- Dado un YAML válido con todos los campos, verificar que se parsea correctamente.
- Validar que cada campo del struct `Config` refleja el valor del YAML.
- Usar un archivo temporal creado con `os.CreateTemp`.

### 1.2 Valores por defecto

```go
func TestLoadConfig_Defaults(t *testing.T)
```

- Dado un YAML mínimo (solo `provider.api_key`), verificar que los defaults se aplican:
  - `agent.max_iterations` = 10
  - `agent.history_length` = 20
  - `agent.memory_results` = 5
  - `agent.max_tokens_per_turn` = 4096
  - `provider.timeout` = 60s
  - `provider.max_retries` = 3
  - `tools.shell.allow_all` = false
  - `tools.file.max_file_size` = "1MB"
  - `tools.http.timeout` = 15s
  - `limits.tool_timeout` = 30s
  - `limits.total_timeout` = 120s
  - `logging.level` = "info"
  - `store.type` = "file"

### 1.3 Resolución de variables de entorno

```go
func TestLoadConfig_EnvVarResolution(t *testing.T)
```

- Table-driven con casos:
  - `${ANTHROPIC_API_KEY}` → resuelve al valor seteado via `t.Setenv`.
  - `${UNDEFINED_VAR}` → produce error de validación (campo requerido vacío).
  - Cadenas sin `${}` → se mantienen literales.
  - `${PARTIAL` (sintaxis rota) → se mantiene literal, no crashea.

### 1.4 Expansión de `~`

```go
func TestLoadConfig_TildeExpansion(t *testing.T)
```

- Verificar que `store.path: "~/.daimon/data"` se expande a `$HOME/.daimon/data`.
- Verificar que `tools.file.base_path: "~/workspace"` se expande correctamente.
- Verificar que paths sin `~` no se modifican.

### 1.5 Validación — falla rápido

```go
func TestLoadConfig_ValidationErrors(t *testing.T)
```

- Table-driven con configs inválidas:
  - `provider.api_key` vacío → error.
  - `provider.type` no reconocido → error.
  - `channel.type` no reconocido → error.
  - `agent.max_iterations` = 0 → error.
  - `agent.max_iterations` negativo → error.
  - `limits.tool_timeout` > `limits.total_timeout` → error (o al menos warning).

### 1.6 Prioridad de archivos de config

```go
func TestLoadConfig_FilePriority(t *testing.T)
```

- Con `--config` flag explícito → usa ese archivo.
- Sin flag, con `~/.daimon/config.yaml` presente → usa ese.
- Sin flag, con `./config.yaml` presente → usa ese.
- Sin ningún archivo → error claro.

---

## 2. `internal/store` — FileStore

### 2.1 Guardar y cargar conversación (round-trip)

```go
func TestFileStore_SaveAndLoadConversation(t *testing.T)
```

- Crear una `Conversation` con ID, mensajes, timestamps.
- `SaveConversation()` → `LoadConversation()` con el mismo ID.
- Verificar que todos los campos son idénticos (deep equal).
- Usar `t.TempDir()` como base path.

### 2.2 Escritura atómica

```go
func TestFileStore_AtomicWrite(t *testing.T)
```

- Guardar una conversación.
- Verificar que no existe archivo temporal residual después del save.
- Verificar que si se interrumpe la escritura (simular con un path de directorio inexistente para el temp), el archivo original no se corrompe.

### 2.3 Cargar conversación inexistente

```go
func TestFileStore_LoadNonExistent(t *testing.T)
```

- `LoadConversation(ctx, "no-existe")` debe devolver un error reconocible (ideally wrapping un sentinel como `ErrNotFound`).
- Verificar con `errors.Is()`.

### 2.4 Listar conversaciones

```go
func TestFileStore_ListConversations(t *testing.T)
```

- Guardar 5 conversaciones con distintos `ChannelID`.
- `ListConversations(ctx, "cli", 3)` → devuelve máximo 3, solo del channel "cli".
- Verificar orden por `UpdatedAt` descendente (más reciente primero).

### 2.5 Listar con limit mayor que existentes

```go
func TestFileStore_ListConversations_LimitExceedsTotal(t *testing.T)
```

- Guardar 2 conversaciones.
- `ListConversations(ctx, "cli", 100)` → devuelve 2, sin error.

### 2.6 Memory — append y search

```go
func TestFileStore_MemoryAppendAndSearch(t *testing.T)
```

- Append 3 `MemoryEntry` con contenidos distintos.
- `SearchMemory(ctx, "keyword", 5)` → devuelve solo las que contienen "keyword" (case-insensitive).
- Verificar orden por recencia (newest first).

### 2.7 Memory — search case-insensitive

```go
func TestFileStore_MemorySearchCaseInsensitive(t *testing.T)
```

- Append entry con contenido "Golang es genial".
- `SearchMemory(ctx, "golang", 5)` → la encuentra.
- `SearchMemory(ctx, "GOLANG", 5)` → la encuentra.
- `SearchMemory(ctx, "GoLang", 5)` → la encuentra.

### 2.8 Memory — respeta limit

```go
func TestFileStore_MemorySearchLimit(t *testing.T)
```

- Append 10 entries que matchean la query.
- `SearchMemory(ctx, query, 3)` → devuelve exactamente 3.

### 2.9 Close es idempotente

```go
func TestFileStore_CloseIdempotent(t *testing.T)
```

- Llamar `Close()` dos veces no debe paniquear ni devolver error en la segunda llamada.

### 2.10 Conversaciones sobreviven restart

```go
func TestFileStore_PersistenceAcrossInstances(t *testing.T)
```

- Crear FileStore A, guardar conversación, cerrar A.
- Crear FileStore B con el mismo path, cargar la misma conversación.
- Verificar que los datos son idénticos.

---

## 3. `internal/tool` — Tools

### 3.1 Tool Registry

```go
func TestToolRegistry_RegisterAndGet(t *testing.T)
```

- Registrar un mock tool con nombre "test_tool".
- `Get("test_tool")` → devuelve el tool.
- `Get("no_existe")` → devuelve nil o error.

```go
func TestToolRegistry_ListAll(t *testing.T)
```

- Registrar 3 tools.
- `All()` → devuelve los 3.
- Verificar que la lista de `ToolDefinition` tiene Name, Description y Schema correctos.

```go
func TestToolRegistry_DuplicateNamePanicsOrErrors(t *testing.T)
```

- Registrar dos tools con el mismo nombre → debe fallar o paniquear (decisión de diseño).

### 3.2 shell_exec

```go
func TestShellExec_WhitelistedCommand(t *testing.T)
```

- Config con whitelist: `["ls", "echo", "cat"]`.
- Ejecutar `echo hello` → `ToolResult{Content: "hello\n", IsError: false}`.

```go
func TestShellExec_BlockedCommand(t *testing.T)
```

- Config con whitelist: `["ls", "echo"]`.
- Ejecutar `rm -rf /` → `ToolResult{IsError: true}` con mensaje indicando que "rm" no está en la whitelist.
- Verificar que el comando NO se ejecutó (el mensaje de error es suficiente prueba).

```go
func TestShellExec_AllowAll(t *testing.T)
```

- Config con `allow_all: true`.
- Ejecutar `date` (no está en whitelist) → funciona correctamente.

```go
func TestShellExec_OutputTruncation(t *testing.T)
```

- Ejecutar un comando que genere >10KB de output (e.g., `seq 1 100000`).
- Verificar que el resultado tiene ≤10KB y termina con "(output truncated)".

```go
func TestShellExec_Timeout(t *testing.T)
```

- Crear contexto con timeout de 100ms.
- Ejecutar `sleep 10`.
- Verificar que retorna antes de 10s con error de timeout.

```go
func TestShellExec_CommandParsing(t *testing.T)
```

- Table-driven:
  - `"ls -la /tmp"` → base command = "ls"
  - `"  echo   hello  "` → base command = "echo" (trims spaces)
  - `""` → error (comando vacío)
  - `"ls && rm -rf /"` → base command = "ls" pero ojo con command chaining (importante: si se usa `exec.Command` con split, esto no es un riesgo; si se usa `sh -c`, sí lo es).

```go
func TestShellExec_WorkingDirectory(t *testing.T)
```

- Config con `working_dir` apuntando a un temp dir.
- Ejecutar `pwd` → el output debe ser el temp dir.

### 3.3 read_file

```go
func TestReadFile_Success(t *testing.T)
```

- Crear archivo en base_path con contenido conocido.
- Leer → contenido correcto.

```go
func TestReadFile_PathTraversal(t *testing.T)
```

- Table-driven con paths maliciosos:
  - `"../../../etc/passwd"` → error, path escapa base_path.
  - `"subdir/../../etc/passwd"` → error.
  - `"/etc/passwd"` (path absoluto) → error.
  - `"./normal/file.txt"` → OK si existe.

```go
func TestReadFile_MaxFileSize(t *testing.T)
```

- Config con `max_file_size: "1KB"`.
- Crear archivo de 2KB → error indicando que excede el límite.

```go
func TestReadFile_NonExistent(t *testing.T)
```

- Leer archivo que no existe → `ToolResult{IsError: true}` con mensaje descriptivo (no Go error).

### 3.4 write_file

```go
func TestWriteFile_Success(t *testing.T)
```

- Escribir a path válido, leer de vuelta, verificar contenido.

```go
func TestWriteFile_CreatesParentDirs(t *testing.T)
```

- Escribir a `"deep/nested/dir/file.txt"` → debe crear los directorios intermedios.

```go
func TestWriteFile_PathTraversal(t *testing.T)
```

- Mismos casos que `read_file` — `"../../escape.txt"` → error.

```go
func TestWriteFile_MaxFileSize(t *testing.T)
```

- Config con `max_file_size: "1KB"`.
- Intentar escribir 2KB → error.

### 3.5 list_files

```go
func TestListFiles_Success(t *testing.T)
```

- Crear estructura de archivos y dirs en base_path.
- Listar → output contiene los nombres esperados.

```go
func TestListFiles_PathTraversal(t *testing.T)
```

- `list_files("../../")` → error.

```go
func TestListFiles_EmptyDir(t *testing.T)
```

- Listar directorio vacío → resultado vacío, no error.

### 3.6 http_fetch

```go
func TestHTTPFetch_GET(t *testing.T)
```

- Levantar `httptest.Server` que devuelve body conocido.
- Ejecutar http_fetch con la URL del test server → contenido correcto.

```go
func TestHTTPFetch_POST(t *testing.T)
```

- Test server que verifica method, body, y headers.
- Ejecutar http_fetch con POST, body, y headers custom → servidor recibe todo correctamente.

```go
func TestHTTPFetch_Timeout(t *testing.T)
```

- Test server que hace `time.Sleep(5s)`.
- Contexto con timeout de 100ms.
- Verificar que retorna error de timeout, no se queda colgado.

```go
func TestHTTPFetch_MaxResponseSize(t *testing.T)
```

- Test server que devuelve 1MB de datos.
- Config con `max_response_size: "1KB"`.
- Verificar que el resultado está truncado.

```go
func TestHTTPFetch_BlockedDomain(t *testing.T)
```

- Config con `blocked_domains: ["evil.com"]`.
- Fetch a `http://evil.com/api` → error indicando dominio bloqueado.

### 3.7 Todos los tools — contrato de interfaz

```go
func TestAllTools_ImplementInterface(t *testing.T)
```

- Para cada tool concreto, verificar que:
  - `Name()` devuelve string no vacío en snake_case.
  - `Description()` devuelve string no vacío.
  - `Schema()` devuelve JSON Schema válido (parseable como `json.RawMessage` y que tiene `"type": "object"`).

```go
func TestAllTools_RespectContextCancellation(t *testing.T)
```

- Para cada tool, ejecutar con un contexto ya cancelado → debe retornar inmediatamente (o casi).

---

## 4. `internal/provider` — Anthropic Provider

### 4.1 Request mapping

```go
func TestAnthropicProvider_RequestMapping(t *testing.T)
```

- Levantar `httptest.Server` que captura el request body.
- Enviar un `ChatRequest` con system prompt, messages, y tools.
- Verificar que el JSON enviado al server tiene:
  - `"model"` correcto.
  - `"system"` con el system prompt.
  - `"messages"` en formato Anthropic (content blocks, no string plano para tool use).
  - `"tools"` con el schema correcto.
  - `"max_tokens"` correcto.

### 4.2 Response parsing — text only

```go
func TestAnthropicProvider_ParseTextResponse(t *testing.T)
```

- Mock server devuelve response con `stop_reason: "end_turn"` y un content block de tipo text.
- Verificar: `ChatResponse.Content` tiene el texto, `ToolCalls` está vacío, `StopReason` = "end_turn".

### 4.3 Response parsing — tool use

```go
func TestAnthropicProvider_ParseToolUseResponse(t *testing.T)
```

- Mock server devuelve response con `stop_reason: "tool_use"` y content blocks mixtos (text + tool_use).
- Verificar: `Content` tiene el texto, `ToolCalls` tiene el tool call con ID/Name/Input correctos.

### 4.4 Response parsing — multiple tool calls

```go
func TestAnthropicProvider_ParseMultipleToolCalls(t *testing.T)
```

- Mock server devuelve 2+ tool_use blocks en un response.
- Verificar que todos se parsean en `ToolCalls`.

### 4.5 Tool result formatting

```go
func TestAnthropicProvider_ToolResultFormat(t *testing.T)
```

- Enviar un `ChatRequest` que contiene un mensaje con `Role: "tool"` y `ToolCallID` seteado.
- Capturar el request al mock server.
- Verificar que se envía como `role: "user"` con content block de tipo `tool_result` y `tool_use_id` correcto.

### 4.6 Headers correctos

```go
func TestAnthropicProvider_Headers(t *testing.T)
```

- Capturar headers en el mock server.
- Verificar presencia de: `x-api-key`, `anthropic-version: 2023-06-01`, `content-type: application/json`.

### 4.7 Error handling — HTTP 429

```go
func TestAnthropicProvider_RateLimitRetry(t *testing.T)
```

- Mock server devuelve 429 las primeras 2 veces, luego 200.
- Verificar que el provider reintenta y eventualmente devuelve respuesta exitosa.
- Verificar que no excede `max_retries` de la config.

### 4.8 Error handling — HTTP 500

```go
func TestAnthropicProvider_ServerError(t *testing.T)
```

- Mock server devuelve siempre 500.
- Verificar que el provider reintenta hasta `max_retries` y luego devuelve error.

### 4.9 Error handling — JSON inválido

```go
func TestAnthropicProvider_InvalidJSON(t *testing.T)
```

- Mock server devuelve 200 con body `"not json"`.
- Verificar que devuelve error descriptivo, no panic.

### 4.10 Context cancellation

```go
func TestAnthropicProvider_ContextCancellation(t *testing.T)
```

- Mock server que bloquea 5 segundos.
- Cancelar contexto después de 100ms.
- Verificar que `Chat()` retorna error de contexto, no se queda colgado.

### 4.11 SupportsTools

```go
func TestAnthropicProvider_SupportsTools(t *testing.T)
```

- `SupportsTools()` debe devolver `true` para el provider de Anthropic.

### 4.12 Usage stats

```go
func TestAnthropicProvider_UsageStats(t *testing.T)
```

- Mock server devuelve response con `usage: {input_tokens: 100, output_tokens: 50}`.
- Verificar que `ChatResponse.Usage` refleja esos valores.

---

## 5. `internal/channel` — CLI Channel

### 5.1 Start es non-blocking

```go
func TestCLIChannel_StartNonBlocking(t *testing.T)
```

- Llamar `Start()` con un pipe como stdin.
- Verificar que retorna en <100ms (no se bloquea leyendo).

### 5.2 Mensajes llegan al inbox

```go
func TestCLIChannel_MessagesRouted(t *testing.T)
```

- Crear pipe, escribir "hello\n" en el write end.
- Verificar que aparece un `IncomingMessage` en el inbox channel con `Text: "hello"`.

### 5.3 Send escribe a stdout

```go
func TestCLIChannel_SendOutput(t *testing.T)
```

- Capturar stdout con un buffer.
- `Send(ctx, OutgoingMessage{Text: "response"})`.
- Verificar que el buffer contiene "response".

### 5.4 Shutdown limpio

```go
func TestCLIChannel_GracefulShutdown(t *testing.T)
```

- Start con un contexto cancelable.
- Cancelar el contexto.
- Verificar que `Stop()` retorna sin error y las goroutines terminan (no goroutine leak).

### 5.5 Name devuelve "cli"

```go
func TestCLIChannel_Name(t *testing.T)
```

- `Name()` == "cli".

---

## 6. `internal/agent` — Agent Loop

Esta es la sección más crítica. El agent loop se testea con mocks de todas las interfaces.

### 6.1 Mocks necesarios

```go
// En agent_test.go

type mockProvider struct {
    responses []*ChatResponse // respuestas en secuencia
    calls     []ChatRequest   // requests recibidos (para assertions)
    callIndex int
}

type mockChannel struct {
    inbox    chan IncomingMessage
    sent     []OutgoingMessage
    startErr error
}

type mockTool struct {
    name     string
    result   ToolResult
    execErr  error
    called   bool
    params   json.RawMessage
}

type mockStore struct {
    conversations map[string]*Conversation
    memories      []MemoryEntry
    saveErr       error
}
```

### 6.2 Flujo simple — text response

```go
func TestAgentLoop_SimpleTextResponse(t *testing.T)
```

- Mock provider devuelve texto "Hello!" con `StopReason: "end_turn"`.
- Enviar un `IncomingMessage` por el inbox.
- Verificar:
  - El provider recibió un `ChatRequest` con el mensaje del usuario.
  - El channel recibió un `Send()` con "Hello!".
  - El store recibió `SaveConversation()`.

### 6.3 Flujo con tool use — un ciclo

```go
func TestAgentLoop_SingleToolCall(t *testing.T)
```

- Mock provider primera llamada: devuelve `ToolCall{Name: "shell_exec", ...}` con `StopReason: "tool_use"`.
- Mock provider segunda llamada: devuelve "Done!" con `StopReason: "end_turn"`.
- Mock tool "shell_exec": devuelve `ToolResult{Content: "file1.txt\nfile2.txt"}`.
- Verificar:
  - Provider fue llamado 2 veces.
  - Tool fue ejecutado con los params correctos.
  - La segunda llamada al provider incluye el tool result.
  - Channel recibió "Done!" como respuesta final.

### 6.4 Flujo con tool use — múltiples ciclos

```go
func TestAgentLoop_MultipleToolCalls(t *testing.T)
```

- Secuencia: tool_call → tool_result → tool_call → tool_result → text final.
- Provider devuelve 3 responses en secuencia (tool, tool, text).
- Verificar que el provider fue llamado 3 veces con contexto acumulativo correcto.

### 6.5 Max iterations enforcement

```go
func TestAgentLoop_MaxIterationsReached(t *testing.T)
```

- Config: `max_iterations: 3`.
- Provider siempre devuelve tool calls (nunca "end_turn").
- Verificar:
  - Provider fue llamado exactamente 3 veces (no más).
  - Channel recibe respuesta parcial o mensaje de "(iteration limit reached)".

### 6.6 Total timeout enforcement

```go
func TestAgentLoop_TotalTimeout(t *testing.T)
```

- Config: `total_timeout: 200ms`.
- Mock provider con `time.Sleep(100ms)` por llamada.
- Mandar un mensaje que requiere 5+ iteraciones.
- Verificar que el loop termina antes de completar todas las iteraciones.

### 6.7 Tool timeout enforcement

```go
func TestAgentLoop_ToolTimeout(t *testing.T)
```

- Config: `tool_timeout: 50ms`.
- Mock tool que hace `time.Sleep(1s)` en Execute.
- Verificar que el resultado enviado al provider es un error de timeout, no el resultado del tool.

### 6.8 Tool panic recovery

```go
func TestAgentLoop_ToolPanicRecovery(t *testing.T)
```

- Mock tool que hace `panic("crash!")` en Execute.
- Verificar:
  - El agent loop NO crashea.
  - El provider recibe un tool result con `IsError: true` y contenido "Tool crashed".
  - El loop continúa procesando.

### 6.9 Context building — system prompt

```go
func TestAgentLoop_ContextBuildSystemPrompt(t *testing.T)
```

- Config con `agent.personality` definido.
- Verificar que el `ChatRequest` enviado al provider tiene `SystemPrompt` que incluye la personality.

### 6.10 Context building — memory injection

```go
func TestAgentLoop_ContextBuildMemory(t *testing.T)
```

- Store con memories relevantes.
- Verificar que el system prompt incluye una sección "Relevant Memory" (o similar) con el contenido de las memories.

### 6.11 Context building — history truncation

```go
func TestAgentLoop_HistoryTruncation(t *testing.T)
```

- Config: `history_length: 5`.
- Conversación existente con 20 mensajes.
- Verificar que el `ChatRequest` solo incluye los últimos 5 mensajes (más el primero del usuario, según spec).

### 6.12 Context building — tool definitions

```go
func TestAgentLoop_ToolDefinitionsIncluded(t *testing.T)
```

- Registrar 3 tools.
- Verificar que `ChatRequest.Tools` contiene las 3 tool definitions con Name, Description y Schema correctos.

### 6.13 Store failure no bloquea respuesta

```go
func TestAgentLoop_StoreFailureDoesNotBlockResponse(t *testing.T)
```

- Mock store que devuelve error en `SaveConversation()`.
- Verificar que el channel igualmente recibe la respuesta del provider.
- Verificar que el error se logueó (se puede verificar con un logger custom o simplemente que no panic).

### 6.14 Provider error — comunicado al usuario

```go
func TestAgentLoop_ProviderErrorReportedToUser(t *testing.T)
```

- Mock provider que devuelve error.
- Verificar que el channel recibe un mensaje de error legible (no un stack trace).

### 6.15 Multiple tool calls en un response

```go
func TestAgentLoop_ParallelToolCallsInSingleResponse(t *testing.T)
```

- Provider devuelve response con 2 tool calls simultáneos.
- Ambos tools se ejecutan.
- Los resultados de ambos se envían al provider en la siguiente llamada.

---

## 7. Tests de Integración

Estos tests verifican el flujo end-to-end sin mockear componentes internos (excepto el provider, que requiere un servidor HTTP).

### 7.1 Flujo completo CLI → Agent → FileStore

```go
func TestIntegration_FullCLIFlow(t *testing.T)
```

- Componentes reales: CLI channel (con pipes), FileStore (con temp dir), tools reales.
- Mock: solo el provider (httptest server).
- Flujo:
  1. Escribir "list my files" en stdin pipe.
  2. Provider responde con tool_call a `list_files`.
  3. Tool ejecuta contra el filesystem real.
  4. Provider recibe resultado y responde con texto.
  5. Verificar que stdout tiene la respuesta.
  6. Verificar que la conversación se guardó en disco.

### 7.2 Conversación persistente

```go
func TestIntegration_ConversationSurvivesRestart(t *testing.T)
```

- Ejecutar un flujo completo (como 7.1) y guardar.
- Crear nueva instancia de agent con el mismo store path.
- Cargar la conversación.
- Verificar que el historial completo está presente.

### 7.3 Extensibilidad — agregar tool sin tocar agent loop

```go
func TestIntegration_AddNewTool(t *testing.T)
```

- Definir un tool custom "echo_tool" que simplemente devuelve lo que recibe.
- Registrarlo en el registry.
- Verificar que el provider lo ve en las tool definitions.
- Verificar que el agent loop lo ejecuta correctamente cuando el provider lo invoca.
- **Confirmar que no se tocó ningún código en `internal/agent/`** (este es un test conceptual; en la práctica, se verifica que el tool se registra y funciona solo con implementar la interfaz).

---

## 8. Tests de Seguridad

### 8.1 Shell injection vectors

```go
func TestSecurity_ShellInjection(t *testing.T)
```

- Table-driven con inputs maliciosos:
  - `"ls; rm -rf /"` → solo "ls" se valida contra whitelist. Si se usa `sh -c`, esto es peligroso.
  - `"ls | cat /etc/passwd"` → similar.
  - `"$(whoami)"` → command substitution.
  - `` "`whoami`" `` → backtick substitution.
- **La aserción depende de la implementación**: si usa `exec.Command("ls", "-la")` (split), estos ataques no funcionan. Si usa `exec.Command("sh", "-c", input)`, SÍ funcionan y el test debe fallar.

### 8.2 Path traversal exhaustivo

```go
func TestSecurity_PathTraversal(t *testing.T)
```

- Table-driven, todos deben ser rechazados:
  - `"../secret"`
  - `"./../../etc/passwd"`
  - `"/etc/passwd"` (absoluto)
  - `"subdir/../../../etc/passwd"`
  - `"....//....//etc/passwd"` (doble dot bypass)
  - `"subdir/./../../etc/passwd"`
  - Paths con null bytes: `"file\x00.txt"` (si el OS lo permite).
  - Symlink que apunta fuera del base_path (crear symlink en test, verificar que el read lo rechaza).

### 8.3 File size limits

```go
func TestSecurity_FileSizeLimits(t *testing.T)
```

- Verificar que `read_file` y `write_file` respetan `max_file_size`.
- Verificar que `http_fetch` respeta `max_response_size`.
- Verificar que `shell_exec` respeta el límite de 10KB de output.

---

## 9. Tests de Rendimiento / Resource Budget

### 9.1 Binary size

```go
// Este es un test de build, no de runtime.
// Se ejecuta como parte del CI.
```

```bash
#!/bin/bash
# test_binary_size.sh
CGO_ENABLED=0 go build -ldflags="-s -w" -o /tmp/daimon ./cmd/daimon
SIZE=$(stat -c%s /tmp/daimon 2>/dev/null || stat -f%z /tmp/daimon)
MAX=$((15 * 1024 * 1024))  # 15MB
if [ "$SIZE" -gt "$MAX" ]; then
    echo "FAIL: binary size ${SIZE} exceeds 15MB limit"
    exit 1
fi
echo "PASS: binary size ${SIZE} bytes"
```

### 9.2 Startup time

```go
func TestPerformance_StartupTime(t *testing.T)
```

- Ejecutar el binario con `--help` o un flag de version.
- Medir tiempo de ejecución.
- Verificar que es <500ms.
- (Nota: este test es más relevante en CI con hardware consistente.)

### 9.3 Idle memory

```go
func TestPerformance_IdleMemory(t *testing.T)
```

- Iniciar el agent con todos los componentes pero sin procesar mensajes.
- Leer RSS del proceso via `/proc/self/status` o `runtime.ReadMemStats()`.
- Verificar que es <50MB.
- (Nota: `runtime.ReadMemStats()` da heap del Go runtime; RSS es más preciso pero OS-dependent.)

### 9.4 Operating memory bajo carga

```go
func TestPerformance_OperatingMemory(t *testing.T)
```

- Procesar 10 mensajes secuenciales con tool calls (usando mock provider).
- Medir memoria máxima durante el proceso.
- Verificar que se mantiene <150MB.

---

## 10. Tests Estructurales (Arquitectura)

### 10.1 Agent loop no importa implementaciones concretas

```bash
# test_imports.sh — verificar en CI
# El paquete internal/agent NO debe importar internal/channel/cli,
# internal/provider/anthropic, internal/tool/shell, etc.

IMPORTS=$(go list -f '{{.Imports}}' ./internal/agent/...)
if echo "$IMPORTS" | grep -q "internal/channel/cli\|internal/provider/anthropic\|internal/tool/shell\|internal/tool/fileops\|internal/tool/httpfetch\|internal/store/filestore"; then
    echo "FAIL: agent loop imports concrete implementations"
    exit 1
fi
echo "PASS: agent loop only depends on interfaces"
```

### 10.2 Lines of code budget

```bash
# test_loc.sh
LOC=$(find . -name '*.go' -not -name '*_test.go' | xargs wc -l | tail -1 | awk '{print $1}')
if [ "$LOC" -gt 3000 ]; then
    echo "FAIL: $LOC lines exceeds 3000 line budget"
    exit 1
fi
echo "PASS: $LOC lines"
```

### 10.3 Coverage target

```bash
# test_coverage.sh
go test -coverprofile=coverage.out ./internal/...
COVERAGE=$(go tool cover -func=coverage.out | grep total | awk '{print $3}' | tr -d '%')
THRESHOLD=70
if (( $(echo "$COVERAGE < $THRESHOLD" | bc -l) )); then
    echo "FAIL: coverage $COVERAGE% below $THRESHOLD%"
    exit 1
fi
echo "PASS: coverage $COVERAGE%"
```

---

## 11. Tests de Concurrencia

### 11.1 Goroutine leaks

```go
func TestAgent_NoGoroutineLeaks(t *testing.T)
```

- Contar goroutines antes de crear el agent.
- Crear agent, procesar un mensaje, hacer shutdown.
- Contar goroutines después.
- Verificar que la diferencia es 0 (o ≤1 por goroutines de runtime).
- Usar `runtime.NumGoroutine()` con un pequeño `time.Sleep` para dar tiempo al cleanup.

### 11.2 Shutdown limpio bajo mensaje en proceso

```go
func TestAgent_ShutdownDuringProcessing(t *testing.T)
```

- Enviar un mensaje que activa un tool call lento (mock tool con 500ms sleep).
- Cancelar el contexto después de 100ms.
- Verificar que el agent se detiene sin panic y sin goroutine leaks.

---

## Resumen — Mapping a Definition of Done

| Requisito DoD | Tests que lo cubren |
|---|---|
| Binary <15MB | 9.1 |
| Idle <50MB RSS | 9.3 |
| Operating <150MB | 9.4 |
| CLI channel funciona | 5.1–5.5, 7.1 |
| Multi-turn tool use | 6.3, 6.4, 6.15 |
| 5 tools funcionan | 3.2–3.6 |
| Shell whitelist | 3.2 (BlockedCommand, AllowAll) |
| File sandboxing | 3.3–3.5 (PathTraversal), 8.2 |
| Conversaciones persisten | 2.1, 2.10, 7.2 |
| Memory funciona | 2.6–2.8, 6.10 |
| Config + env vars | 1.1–1.6 |
| Iteration/timeout limits | 6.5, 6.6, 6.7 |
| >70% coverage | 10.3 |
| Tool extensibility <50 lines | 7.3 |
| <3000 LOC | 10.2 |

---

## 12. New Capabilities — Definition of Done (provider-model-selection-refactor)

### 12.1 `provider-model-discovery`

- `GET /api/providers/{p}/models` returns 200 with `X-Source: live` on cold miss, `X-Source: cache` on warm hit within TTL, `X-Source: cache-stale` when fetch fails but stale entry exists, `X-Source: fallback` when fetch fails and no cache exists.
- `GET /api/providers/{known-but-unconfigured}/models` returns 401.
- `GET /api/providers/{unknown}/models` returns 404.
- `OllamaProvider.ListModels()` parses `GET /api/tags` correctly; returns models with `Free: true`; returns error (no panic) on non-200 or connection refused.
- Startup model validation warns (via `slog.Warn`) when configured model is not in the live list; never blocks startup or returns an error.

### 12.2 `reasoning-stream`

- OpenRouter stream parser emits `StreamEventReasoningDelta` from `delta.reasoning_content` / `delta.reasoning` fields; does not mix with `TextDelta`.
- Anthropic stream parser emits `StreamEventReasoningDelta` from `content_block_start{type:"thinking"}` + `thinking_delta` events; does not append thinking content to assembled `ChatResponse.Content`.
- Agent loop (`processStreamingCall`) calls `sw.WriteReasoning(text)` for each `ReasoningDelta` event; does NOT accumulate into assembled content; finalizes the writer even for reasoning-only responses (no leak).
- WriteReasoning failure is non-fatal (slog.Debug); text streaming continues unaffected.

### 12.3 `chat-thinking-ui`

- `<ThinkingBlock>` renders with content visible while streaming (`isStreaming=true, hasTextStarted=false`); shows "Thinking..." label.
- Auto-collapses (shows "Thought for Xs" label) when `hasTextStarted=true`.
- Toggleable via click or Enter/Space key after collapse.
- Renders `null` when `reasoning` prop is empty or undefined.
- `ChatPage.tsx` accumulates `reasoning_token` WS frames into `reasoningBuffer`; passes `hasTextStarted`, `thinkingStartedAt`, `textStartedAt` to `<ThinkingBlock>`.
- No `ThinkingBlock` in DOM when no `reasoning_token` frames precede text frames.

---

## 13. Vitest patterns

### ESM binding quirk — spy call assertions vs DOM effects

When a module is mocked with `vi.mock('../../api/client', factory)` and the same module is imported by a page component via a different relative path (e.g. `'../api/client'`), Vitest may resolve a different ESM binding. This causes spy call count assertions to be flaky (the spy exists but the count stays 0 even when the function ran).

**Prefer asserting DOM effects** (e.g. `expect(screen.getByRole('listbox')).toBeInTheDocument()`) over asserting spy call counts when testing page-level interactions. Only assert spy calls when the import path in the test and the import path in the component are guaranteed identical.

### Global `@tanstack/react-virtual` mock

Place the mock at `src/__mocks__/@tanstack/react-virtual.ts`. jsdom has no layout engine — `useVirtualizer` always returns 0 visible items without this mock. The global mock returns up to 12 items (simulating a visible window) so test assertions can find list options. Individual test files still call `vi.mock('@tanstack/react-virtual')` to activate it — but they can omit the factory argument; the global mock file is used automatically.