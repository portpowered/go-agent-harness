
# Files (TODO)
We want the llms to operate on our files, but if the files are too large, the providers won't accept them. 

The llm apis provided by anthropic/openai/google/openrouter etc provide a way to upload files as a way to handle this. 

# How files are used as part of agent CLI. 

Files are automatically uploaded to the model provide as part of the runtime of the agent cli. 
When you as a customer do `agent ask "who's this actor?" ./file.jpeg`, the agent cli will intelligently upload the file to the model provider if necessary. 

Users can manage the files that are uploaded to the model provider by using the `agent files` commands. 

There are four agent file commands. 
`agent files list` - list the files that are uploaded to the model provider.
`agent files show` - show the details of a file that is uploaded to the model provider. We present the internal file id, the corresponding internal calculated hash, the size, the mime type, the file name, etc. 
`agent files upload` - upload a file to the model provider.
`agent files download` - download a file from the model provider.
`agent files delete` - delete a file from the model provider.

## Internal implementation details

As part of the go-agent-gateway package, each provider may choose to implement a files provider API. 
During inference, the agent cli will use the files provider implementation of the provider to upload files as necessary. 
The agent cli knows to upload files by checking the provider config, and determining if the provider supports files of said media type and size. 
If the provider does not support files of said media type and size, the agent cli will fail and return a bad request message. 
If the file is small enough, the agent cli will pass the file as part of the request to the provider. 

The agent CLI will try to upload the file if necessary to the provider if the provider supports checking.

### Handling repetitive file uploads

The agent CLI sort of has to be intelligent about handling file uploads, 
the current APIs from claude and openai don't expose etags or cached file ids, so we have to maintain a sort of mapping between the file id and the provider cache. 
Locally we keep a file in the config directory called `files.json`. 

The structure of the files.json looks as follows: 

{
    "files": [
        {
            "id": "internal-file-id",
            "provider": "anthropic",
            "provider_file_id": "provider-specific-file-id",
            "hash": "sha256-hash-of-the-file",
            "size": 1024,
            "mime_type": "image/jpeg",
            "file_name": "file.jpeg", 
            "created_at": "2026-02-27T12:00:00Z"
        }
    ]
}

whenever the agent cli is recommended to upload a file, it will check the files.json to see if the file has already been uploaded or not. 
if the file already has been uploaded for that provider, it will use the referenced file id instead of uploading again. 

### Message stream for file handling

During message handling, the agent cli will send events to the message stream to indicate that a file is being uploaded. 

The events will be of the following types:
- FILE.START - indicates that a file is being uploaded. It will include the media type and the file name.
- FILE.ERROR - indicates that the file upload failed. It will include the error message. This does not mean that the stream is supposed to fail, and the stream may continue even with it happening.
- FILE.END - indicates that the file has been uploaded.

Errors have the following data: 
- reason for failure 
- a message describing the error

File upload references are based on the message on the wire being a file rather than a baseline context. 

### Configuration

as part of the agent cli configuration, customers can define whether or not they want to use files APIs as part of the model inference. 
By setting `--no-provider-file-apis` as part of the agent cli flags, or `use_provider_file_apis: false`, then we will not use the files APIs as part of the model inference. 

### Known error types: 
- FILE_TOO_LARGE - the file is too large to be uploaded.
- RATE_LIMIT_EXCEEDED - the file upload is throttled.
- UNKNOWN_ERROR - an unknown error occurred.
- BAD_REQUEST - the request was malformed.
- INTERNAL_SERVER_ERROR - an internal server error occurred from the file API provider. 

# references
- https://platform.claude.com/docs/en/build-with-claude/files
- https://developers.openai.com/api/reference/resources/files
- https://ai.google.dev/gemini-api/docs/files

# Task breakdown

1. implement the files providers for gemini, openai, and anthropic. 
2. update the inferencing loop for gemini, openai, and anthropic to support file handling
3. update the agent cli configuration to handle the file.json config. 
4. update the agent cli to support the file commands