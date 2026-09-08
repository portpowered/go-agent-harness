# Submission lessons and proposed improvements

## Code submission

1. Fetch main before the first preflight and again before merge. Separate inherited
   failures from new failures by comparing the exact base and candidate commits.
   This run initially carried an upstream baseline-history defect; main later
   supplied the reviewed fix. Do not rewrite baseline provenance or raise limits.
2. Run the repository's actual pinned lint and architecture commands before pushing,
   not only build/vet and focused tests. `errcheck.check-blank=true` means assigning
   an error to `_` is not a fix. Propagate production close failures and report
   fixture write failures. Check complexity and physical package/file limits early.
3. Keep behavior-preserving refactoring separate from new behavior and rerun tests
   against the integrated revision. Include cancellation, exact-target focus leases,
   partial upload, pending-media and duplicate-publish tests in the preflight.
4. Treat CI as an acceptance gate, not a notification. Inspect each failed step's
   log, rerun on the final commit, and merge only that checked commit. Preserve the
   branch and evidence rather than bypassing checks during an unattended run.

## Media preparation and publication

1. Add a future read-only recovery/status command exposing bounded upload state
   (token, bytes received, attached flag, expected account), never raw media or
   credentials. It would avoid ad hoc browser diagnostics after a generation race.
2. Wait for the initial tool catalog to stabilize before allocating an upload.
   Test navigation/catalog changes at every stage. A failed prepare must not cause
   an automatic publish or an unverified retry; attached media must be preserved.
3. Add real-X multiline-caption fixtures before promising paragraph support. Until
   then prefer single-line captions. Reject changed text rather than normalizing
   away a mismatch after user review. Clear only an exactly identified owned draft.
4. Keep a hash-bound release manifest: source references, prompt, render settings,
   final export hash, review mode/limitations, caption/account, prepare receipt,
   publish receipt and permalink playback result. Composer-cleared is not proof
   that a durable post contains a playable video. Never retry an uncertain publish
   without inspecting the account for the existing post.

## Content review

1. Evaluate likeness in the generated video, not just its source image. Full-body
   references diluted facial detail; direct-photo conditioning at `max` reference
   detail worked better here. Preserve the user's original as the comparison anchor.
2. Replace expected-word coverage alone with exact normalized transcript comparison
   plus explicit extra/missing-word reporting. Coverage 1.0 can still contain an
   unwanted leading or trailing word; conflicting ASR results require investigation.
3. Re-review the exact exported cut after trimming or transcoding, including its
   first and last decoded frames. Record original-to-export time ranges and hashes.
4. Capability-check reviewers up front. Sampled frames and transcription do not
   satisfy continuous audiovisual cold-watch. Label the review mode explicitly;
   never infer frozen video from silence or voice quality from text. Require genuine
   audiovisual review if strict lip-sync/sound-quality approval is mandatory.
5. Keep publication count bounded and AI disclosure explicit. Daily automation
   should remain disabled until retry recovery and review gates are dependable.

These are process recommendations. This change fixes the failing code checks and
integrates upstream's history correction; it does not claim the proposed recovery
command, multiline support, exact-transcript gate or daily automation is implemented.
