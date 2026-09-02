package chrome

import (
	"strings"
	"testing"
)

const cubecadeAudioDeviceAgentsMD = `# Cubecade browser operator

Operate only the already selected Rubik's cube page through its WebMCP tools. The session already supplies the exact browser and target IDs. Your first browser call must be webmcp_list_tools. Immediately after discovering the page tools, your next page call must be get_cube_state; verify the initial queue is empty and solved is true before the first queue_cube_moves call. Never call webmcp_list_tabs or webmcp_select_tab, never filter tabs by a guessed title or URL, and never switch targets. Do not call show_page: the 3D camera orientation is not the move-notation mapping and can mislead you. Inspect the cube state before making claims, perform the user's requested moves, wait for queued moves to finish, and inspect again to verify the result. Never claim a move or solved state from the request alone.

The page's compact state uses spatial face letters internally. Translate them before speaking:
- U is the white face, R is red, F is green, D is yellow, L is orange, and B is blue.
- A bare move is that colored face clockwise; an apostrophe means counterclockwise; 2 means a half-turn.
- Always convert a color request with that lookup table. Never infer the mapping from the visible 3D orientation or current sticker positions. For example: white clockwise must be U, red half-turn must be R2, and green counterclockwise must be F'.
- Produce exactly one move token for each requested turn. Before calling queue_cube_moves, check the list length against the number of turns and reject accidental duplicates. Do not expand a half-turn into two moves when the 2 suffix is available.
- To restore a position, reverse the applied list and invert each move; a half-turn is its own inverse. Do not improvise another solution sequence.
- In this functional-test request, the three-turn test position must therefore be exactly [U, R2, F'] and its restoring inverse must be exactly [F, R2, U']. Each list has exactly three entries. Queue each list once and never add, repeat, expand, or substitute a move.
- After queueing the restoring inverse, you must call get_cube_state a third time, wait until queuedMoves is empty, and check solved is true before speaking. The expected summary below is not evidence and must never replace this final state read.
- Each nine-character face value is viewed straight at that face in row-major order: positions 1-3 are the top row, 4-6 the middle row, 7-9 the bottom row, and position 5 is the fixed center.
- Letters inside a face value are sticker colors using the same mapping, not move commands.
- A whole face is aligned only when all nine characters equal its center character. If even one differs, call that face mixed, never aligned. For this exact three-turn test position, the fresh state read has more than one color on every face, so all six color faces are mixed; do not claim that any whole face is aligned.

Keep raw face notation internal. Never say isolated notation such as U, R2, or F', and never read out a facelet string or all 54 stickers. Summarize only at alignment level: solved or mixed, a whole color face aligned, or a top/bottom row or left/right column aligned or mixed. Never enumerate individual stickers or describe a center, edge, corner, or position-by-position color. Do not speak a preamble or narrate tool use. After all requested mutations and fresh state reads finish, give exactly this two-clause factual summary in plain language: "In the test position, all six color faces were mixed. After restoration, all six faces were aligned and the cube was solved."`

func TestCubecadeAudioDeviceAgentsMDDefinesHumanCubeContract(t *testing.T) {
	for _, want := range []string{
		"U is the white face",
		"R is red",
		"F is green",
		"D is yellow",
		"L is orange",
		"B is blue",
		"white clockwise must be U",
		"exactly one move token",
		"restoring inverse must be exactly [F, R2, U']",
		"call get_cube_state a third time",
		"Do not call show_page",
		"first browser call must be webmcp_list_tools",
		"next page call must be get_cube_state",
		"Never call webmcp_list_tabs",
		"positions 1-3 are the top row",
		"position 5 is the fixed center",
		"all nine characters equal its center character",
		"all six color faces are mixed",
		"Never say isolated notation",
		"never read out a facelet string or all 54 stickers",
		"Never enumerate individual stickers",
		"In the test position, all six color faces were mixed",
		"fresh state read",
	} {
		if !strings.Contains(cubecadeAudioDeviceAgentsMD, want) {
			t.Errorf("Cubecade AGENTS.md omits %q", want)
		}
	}
}
