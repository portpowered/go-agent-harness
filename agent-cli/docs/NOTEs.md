# TODO: 
- small models tend to stop early, need a mechanism to prod the model to continue generating (TODO subsystem)
- small models tend to have repetition issues, need mechanism to set up repetition penalty (TODO subsystem)
-- we need basically a new subsystem that we store state in the loop state for operations to perform, and then during coordinator subsystem, if there is a message in the todo operations, then we submit the message to the current system buffer. then the coordinator during the system buffer if the model completes their message but there is some system message, then it should re-invoke the inferene with the new system message. 

- we have a hard time reading what happens when you have lots of models running at the same time, need a better visualization paradigm for seeing what the people are doing. 
-- one way is that the terminal should be able to show all related sessions and their statuses. 
-- ctrl left + ctrl right to switch between sessions/subsessions

- need a way to to have the pattern do "/load <skill>", run it as part of the chat interaction as well as with autocomplete. 
- need a way to have the pattern of running the "ask" command with a specific set of skills. 
- need a better way to specify the model agents, so that we can say agent load "agent", and have it run with a specific model/provider/default skills. 
- one problem is that we want the model to be able to dispatch sub agents with separate context, so the tool call would look like "agent(skill="typesetter|translator|formatter|context_parser", command="please translate file A into file B"). 

- we were having issues wherein the agent was not able to read t
he file when it was a webp, but it could with png, need a mechanism to handle when the model can/cant read the file. 
-- probably define acceptable mimetypes in the model config, and check against that rather than uploading the file. 
-- need a way to handle bad data inputs, and acceptably handle

- errors from the session context need to be presented better to the user, right now its just hanging out at the bottom, need a proper error stream, that gets filtered out. 

- we were running a local model, and local doesn't need auth so we should create a new configuration type for local models without auth, but the backend openai code should be the same.

- we need to support falai/local image models which take in an input audio/text and generate out an image/audio. in the case where we target stdout, the stdout should be the raw image/audio so that we can pipe it to a file directly. 
-- basically "agent ask "generate an image of a banana" --output-format image/audio > banana.png".
-- add test for this behavior
-- we should manually test this behavior with a local model to validate this behavior
-- https://huggingface.co/Qwen/Qwen3-TTS-12Hz-0.6B-Base
-- create an openai audio compatible endpoint wrapping the model from this package. 
-- https://openrouter.ai/openai/gpt-audio/api

- we should add a new inferencer type to the cli for "local" that allows inference to run against a local model that runs openai, and has no key associated with it. 
-- these should share the llm-gateway with openai, but require no auth key. 

- we need to parse refusal messages from the model, and handle them appropriately as part of the parsing step. 
-- we should have an integration test validating that the agent-cli visualizes the refusal message appropriately. 
