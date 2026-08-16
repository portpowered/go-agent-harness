# Ordered tool profile

Make exactly two tool calls in this order: first call `file_read` for `fixture.txt`, then call `shell_command` with a harmless command that uses the result returned by `file_read`. The second call must be informed by the first result. After both calls, give a concise response and make no more tool calls.
