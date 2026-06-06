# Go Agent Loop

The go agent loop is a library for running an AI agent. 

It is a portable small loop that is intended for use in various ways. 

## Quick start


```
cd your-project-directory
go mod init your-project-name
go get github.com/portpowered/go-agent-loop

```

then use the go agent loop in your code:
```
import (
	"github.com/portpowered/go-agent-loop"
)

func main() {
	loop := agentloop.New()
	out := loop.Run("what is two plus two?")
    fmt.Println(out.GetText())
    // "2+2 = 4"
}
```

# Other Examples


## streaming delta 

```go
func main() {
    loop := agentloop.New()
    streamOut, _ := loop.StreamExecute(context.Background(), 
        agentloop.NewExecuteInput(Message: "what is two plus two?"))
    while delta := streamOut.ReadTextDelta(); delta != nil {
        fmt.Print(delta.Text)
    }
}
```


## async
```go
func main() {
    
	loop := agentloop.New(withConsoleOut(stdout))
    ctx, cancel := context.WithCancel(context.Background())
    go loop.Run(ctx)
    loop.Send(ctx, 
        []messages.Message{
            messages.NewText(messages.RoleUser, "what is two plus two?")})
    // console out will print the response text from the model. 
    loop.Send(ctx, 
        []messages.Message{
            messages.NewText(messages.RoleUser, "okay, now do that twenty times?")})
    // console out will print the response text again
}

```

# Development documents

Start with the [Go Agent Loop Development Guide](docs/development.md) before changing the runtime, message model, streaming protocol, engine ordering, or tests. It covers local commands, downstream verification, and package-specific gotchas.

## Messages

The agent loop uses messages to communicate between its systems. 

There are two variants of messages: full messages and delta messages. 
Full messages are complete messages, intended for direct use.  (message: "Hi there" )
Delta messages are incremental changes to a message, intended to be built up over time.  (messages: "Hi " -> "the" -> "re"). 


When running a loop, the message stream would look like: 

```
[ MESSAGE ] [INDEX 0] [AGENT SYSTEM] 
[ MESSAGE ] [INDEX 1] [AGENT USER] 
[ MESSAGE ] [INDEX 2] [AGENT AGENT] 
[ MESSAGE ] [INDEX 3] [AGENT TOOL] 
[ MESSAGE ] [INDEX 4] [AGENT MODEL] 
```



The agent message delta stream would look like: 

```
[ LOOP.START ] [INDEX 0] [ ACTOR LOOP ] [TICK 0]
[ MESSAGE.START] [INDEX 1] [ ACTOR SYSTEM ] [TICK 1 ]
[ TEXT.START] [INDEX 2] [ ACTOR SYSTEM ] [TICK 2]
[ TEXT.DELTA] [INDEX 3] [ ACTOR SYSTEM ] [TICK 3 ]
[ TEXT.END] [INDEX 4] [ ACTOR SYSTEM ] [TICK 4 ]
[ MESSAGE.END] [INDEX 5] [ACTOR SYSTEM ] [TICK 5 ]
[ MESSAGE.START] [INDEX 6] [ ACTOR USER ] [TICK 6 ]
[ TEXT.START] [INDEX 7] [ ACTOR USER ] [TICK 7 ]
[ TEXT.DELTA] [INDEX 8] [ ACTOR USER ] [TICK 8 ]
[ TEXT.END] [INDEX 9] [ ACTOR USER ] [TICK 9 ]
[ MESSAGE.END ] [INDEX 10] [ ACTOR USER ] [TICK 10 ]
[ MESSAGE.START] [INDEX 11] [ ACTOR AGENT ] [TICK 11 ]
[ TEXT.START ] [INDEX 12] [ ACTOR AGENT ] [TICK 12 ]
[ TEXT.DELTA ] [INDEX 13] [ ACTOR AGENT ] [TICK 13 ]
[ TEXT.END ] [INDEX 14] [ ACTOR AGENT ] [TICK 14 ]
[ TOOLCALL.START ] [INDEX 15] [ ACTOR AGENT ] [TICK 15 ]
[ TOOLCALL.DELTA ] [INDEX 16] [ ACTOR AGENT ] [TICK 16 ]
[ TOOLCALL.END ] [INDEX 17] [ ACTOR AGENT ] [TICK 17 ]
[ MESSAGE.END ] [INDEX 18] [ ACTOR AGENT ] [TICK 18 ]
[ MESSAGE.START ] [INDEX 19] [ ACTOR TOOL ] [TICK 19 ]
[ TEXT.START ] [INDEX 20] [ ACTOR TOOL ] [TICK 20 ]
[ TEXT.DELTA ] [INDEX 21] [ ACTOR TOOL ] [TICK 21 ]
[ TEXT.END ] [INDEX 22] [ ACTOR TOOL ] [TICK 22 ]
[ TOOLCALL.END ] [INDEX 23] [ ACTOR TOOL ] [TICK 23 ]
[ MESSAGE.END ] [INDEX 24] [ ACTOR TOOL ] [TICK 24 ]
[ MESSAGE.START ] [INDEX 25] [ ACTOR MODEL ] [TICK 25 ]
[ TEXT.START ] [INDEX 25] [ ACTOR MODEL ] [TICK 25 ]
[ TEXT.DELTA ] [INDEX 26] [ ACTOR MODEL ] [TICK 26 ]
[ TEXT.END ] [INDEX 27] [ ACTOR MODEL ] [TICK 27 ]
[ MESSAGE.END ] [INDEX 28] [ ACTOR MODEL ] [TICK 28 ]
[ LOOP.END ] [INDEX 29] [ ACTOR LOOP ] [TICK 29 ]
```

### Message types

Inside of each message could be a series of different content. 

Some content types include: 
- TEXT
- TOOLCALL
- AUDIO
- IMAGE
- VIDEO
- FILE
- EMBEDDING
- REASONING

The message types for deltas are like: 
- MESSAGE.START: The start of a message.
- MESSAGE.END: The end of a message.
- MESSAGE.COMPLETE: A complete message, along with all its contents. 
- TEXT.START: The start of text.
- TEXT.DELTA: A delta of text.
- TEXT.END: The end of text.
- TOOLCALL.START: The start of a tool call.
- TOOLCALL.DELTA: A delta of a tool call.
- TOOLCALL.END: The end of a tool call.
- AUDIO.START: The start of audio.
- AUDIO.DELTA: A delta of audio.
- AUDIO.END: The end of audio.
- IMAGE.START: The start of an image.
- IMAGE.DELTA: A delta of an image.
- IMAGE.END: The end of an image.
- VIDEO.START: The start of a video.
- VIDEO.DELTA: A delta of a video.
- VIDEO.END: The end of a video.
- FILE.START: The start of a file.
- FILE.DELTA: A delta of a file.
- FILE.END: The end of a file.
- EMBEDDING.START: The start of an embedding.  
- EMBEDDING.DELTA: A delta of an embedding.
- EMBEDDING.END: The end of an embedding.
- REASONING.START: The start of reasoning.
- REASONING.DELTA: A delta of reasoning.
- REASONING.END: The end of reasoning.
- LOOP.START: the start of the loop. 
- LOOP.END: The end of the loop.
- ERROR: An error occurred.

### Interruptions

Sometimes the user may want to interrupt the loop, for example if they want it to stop. 
Users interrupt the loop by sending a message. 

For example, maybe the user wants to say that the agent is going the wrong way. 
Lets say the loop is at index 16.
To do this: the user sends a message like [ MESSAGE.START ] [INDEX 13] [ACTOR USER]. 

And the loop will wait until the actor submit [ MESSAGE.END ] [INDEX 14] [ACTOR USER]. 
When the actor finishes submitting the message, the loop will continue from index 15. 

The interrupt behavior is configurable by modifying the loop configuration. 
i.e. if you set `NewLoop(withConcurrentUserAndAgent())`, then the loop will not stop parsing messages and the loops will be interleaved. 

### Multimedia

Parsing of the media contents is done specifically by the agent provider. Over the wire, 
the agent loop only sends the media.

#### Audio
For the most part, the agent loop expects to receive audio in PCM 16khz format. 
Other formats are not supported, and you should parse the media media type and convert it to PCM 16khz.

#### Images
Images are expected to be sent as PNGS. 

#### Videos

Videos are expectecd to be sent as straeming H.264. 

#### Files

Files can be any type of file, but the file message contains the file mime type at start.

#### Embeddings

sometimes, we want to pass in embeddings to the model. Embeddings are stored in a file called <>.safetensors. 

## Architecture

The agent loop thinks of the world as a loop. It runs on a ticker. 
The agent loop runs on each tick a series of systems. 
Some systems send messages, some systems record data, some systems process data. 

The agent loop talks to actors. Each actor has a series of input and output buffers. 
The agent loop talks to the actors via the buffers. The actors in turn talk to the agent loop via the same buffers. 

At each loop tick, the agent loop reads the data from the buffers, runs the systems, and writes some data back to the buffers. 

### Why? 

Mostly, we the agent loop works of one idea that says AI agent harnesses are a simulation of inputs and outputs and how they work together. 

If we try to model the agent loop around talking to an LLM alone, then the system looks wrong. The model would have for example tool calls and interrupts be off as sidesystems. Interrupts and concurrency would need to be modelled at small subcomponents rather than as a whole system. 

