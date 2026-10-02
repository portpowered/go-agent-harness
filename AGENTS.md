
Go agents harness is a collection of libraries that are useful to build AI agents in golang. 

## structure
The structure is a multi package workspace that uses go work to build things. 

root
- agent-cli -> tool to call into the agent-cli that can be used to test the agent-loop and llm-gateway
- go-agent-loop -> execution loop harness for the agent
- go-llm-gateway -> wrapper across various AI providers
- tests -> integration tests that evaluate across libraries
- docs -> cross package docs

## architectures

### go-agent-loop

the fundamental structure of the go-agent-loop is created in such a way that is intended to be agnostic to bidirectional/omni models as well as turn based models. 


there is a core agent loop. 
the core agent loop runs on ticks. 
the agent loop has subsystems. 
at each tick the agent loop calls the subsystems. 

the agent loop receives events to trigger ticks. 
the agent may generate ticks. 
the user may generate ticks. 
the system may generat ticks. 

#### participants and the tick loop

Participants (model, tool, user, kernel runners in `pkg/participants`) run on
their own goroutines. The tick loop never calls into them directly: it reads
their outboxes (for the model, `DeltaOutbox`) and writes their inboxes (for the
model, `Inbox` of `InferenceRequest`). Subsystems only see what crosses those
buffers.

- Turn-based mode: the model runner reads one `InferenceRequest` from `Inbox`,
  streams that response to `DeltaOutbox`, and waits for the next request.
- Session mode (`engine.DuplexSession`): the model runner owns a persistent
  provider session and runs its own event loop. That loop selects over provider
  messages, the ordered user-input ingress (`EnqueueSessionInput`: audio,
  control events, and complete messages in one FIFO, with a priority lane for
  RESPONSE.CANCEL), the coordinator's `Inbox`, and the held-onset timer. Ticks
  only see the `DeltaOutbox` stream it produces, and they feed it tool results
  through `ToolResultForwarder` and the ingress.

Barge-in (local speech detection, cancel-before-audio, tool-continuation
exemption, held onset frames) is decided inside the session runner loop, not
in a subsystem. Each decision is made per 20 ms audio frame, between observing
the provider and sending to it, so the RESPONSE.CANCEL reaches the wire before
the interrupting audio. Moving it into a tick would add a goroutine hop
between observing and sending and would lose that ordering. The runner's
lifecycle state is the set of explicit phases in
`participants/internal/sessionstate/`.


## validation

Run `make prepush` before pushing; it tests only the packages your diff affects (`make prepush-full` tests everything, like CI). See docs/local-validation.md.
