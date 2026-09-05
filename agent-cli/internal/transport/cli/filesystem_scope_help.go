package cli

// filesystemPolicyHelp is shared by the two customer-facing commands that
// compose filesystem tools. Keeping the same explanation on both surfaces
// prevents the direct-tool and session paths from advertising different
// boundaries.
const filesystemPolicyHelp = `Filesystem tool scope:
With no --workdir, the effective workdir is the process current directory and relative filesystem-tool paths resolve there. --workdir <directory> selects an existing accessible directory. Repeat --allow-path <directory> to add existing accessible roots; relative allow-path values resolve from the effective workdir and duplicate roots are normalized.
Read and list operations refuse protected system and credential locations even when a broad --allow-path contains them. Shell-command deny-pattern policy is separate; filesystem confinement is not an operating-system sandbox.

Examples:
  allowed: agent --workdir ./project tool write_file path=notes/today.txt content=hello
  refused: agent --workdir ./project tool write_file path=../outside.txt content=hello`
