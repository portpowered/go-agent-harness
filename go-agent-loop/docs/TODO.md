# TODO

## Core Agent Loop

### Interface & Design
- [DONE] Rewrite interface — streaming/turn-taking I/O as structs, not raw messages; stream reader returns a buffer readable by the caller

### Functional Tests
- [DONE] Request → response
- [DONE] Request → agent → tool → response (tool use)
- [DONE] Request → agent → tool → agent → tool → response (multi-turn tool)
- [DONE] Request → agent → batch tools → response (batch tool)
- [DONE] Request → agent → request → agent (turn taking)
- [DONE] Handle interrupts as part of system logic
- [DONE] Interrupt test: request → agent → user interrupt → agent

### Loop Behavior
- Handle loop detection
- [DONE] Handle message retries — resume from last successful token on model failure
- [DONE] Support tick rate and time-per-tick configuration

### Multi agent

### Asynchronous tool / agent system interruption

### Ralph Loop iteration

Fix model, user, runner and agent runner to be able to handle asynchronous responses rather than spawning up the entire response stream. 

---

## Agent Gateway

### Model Support
- Add TTS model support
- Add video model support
- Add support for refusal messages from model inference
- Add concurrent input/output streams for CSM-style multiplexed audio models

### Validation
- Validate at startup that the declared modality is supported by the current model config

---

## Agent CLI

### Model Support
- Add TTS model support
- Add video model support
- Add support for refusal messages from model inference
- Add concurrent input/output streams for CSM-style multiplexed audio models

### Model Configuration
- [DONE] Add configuration file defining supported input/output types per model
- Add config + request flags for target input/output modality
- Add config + request flags for reasoning effort
- Add general model parameter config (top_p, top_k, temperature, etc.)
- Add config for max file size per media type and supported mimetypes
- Add flag to override the supported tools list

### Authentication
- Add OAuth-based auth for OpenAI
- Add OAuth-based auth for Anthropic
- Add HSM support for bootstrapping keys used in calls ()

### System Context & Prompting
- [DONE] Inject system info into initial prompt (OS, current working directory, token limit, model params, active model)
- Warn during inference when approaching token count limit (e.g., at 80% of limit)
- [DONE] Add flag to disable system info injection in initial prompt
- Add flags to control thinking depth / reasoning effort

### Chat Interface
- Add proper TUI to support real-time interrupts during inference
- Support referencing media files as context in the chat tool
- [DONE] make the chat interface print out the model outputs over the stream. 
- [DONE] make th chat interface print out the request inputs into the stream. 
- [DONE] make the chat interface print out the tool use over the stream. 
- [DONE] make the chat interface print out the reasoning trace.
- Make the chat interface support all the various flags from the ask command. 
- make the chat interface have appropriate formatting and presentation 
### Audio Output
- Output audio stream directly to the user's speaker device
- Support raw PCM output to stdout for piping to external audio players
- Support raw media output to stdout for models like nanobana or gpt-audio

### Advanced Execution
- Add skills support
- Add "ralph"-style iterative execution: loops with subagent dispatch within token limits, up to a configurable max iteration count

### Model selection
- add auto model rotation support depending on the input modality and output modality if the model is set to auto/there is a list of models supported in the config. 

### Inputs/Outputs

- [DONE] support stdin of text
- [DONE] Support stdin of audio
- [DONE] Support stdin of image
- Support stdin of video
- Support stdout of audio
- Support stdout of image
- Support stdout of video

---

## Tools

### Read Tool
- Support outputting content in the appropriate format per media type
- [DONE] Support specifying URLs for multimedia input (e.g., Gemini video understanding)

### Web & Browser Tools
- [DONE] Add web fetch tool — retrieve a URL and extract its content
- [DONE] Add web search tool — search the web via pluggable providers (Brave, Tavily, Perplexity, DuckDuckGo)
- Add browser-use tool via CDP for agent-driven browser interaction

### Screen & Device Control
- [DONE] Add screen capture capability
- [DONE] Add device control output as a tool (Windows/Linux/macOS)
- [DONE] Support video capture for screen control
- [DONE] Support video capture during action performance
- [DONE] Support click-and-drag interactions
- [DONE] add keyboard input controls to the screen capture tool
- Support image buffering from screen for streaming image input to the model

### Sleep
- add a tool that lets the model sleep for a specified duration

### Multi agent
Add the agent be able to spawn a new agent, with a corresponding input/output buffer. 

### Skill tool
- Add a new tool to open a skill, and load it into the context. 

---

## Multimedia I/O

### Completed
- [DONE] Input: audio, image, video, file
- [DONE] Output: audio, image, video, file

### Generation Configuration
- Add image generation config (width/height, quality, count, alpha, base image)
- Add video generation config (width/height, fps, duration, sample input audio/image/video)

### Streaming Configuration
- Specify VAD detection configuration for streaming input/output
- Specify language and voice configuration during streaming

### Large File Handling
- Support uploading files that exceed the model's maximum file size

---

## Publishing & Release
- Add goreleaser configs
- Make package public
- Add package versioning mechanisms
- Add package signing for macOS and Windows releases
- Publish library to `go get`
- Update documentation and instructions

---

## Documentation & Examples
- Add usage examples for the library
- Add library documentation
