
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

