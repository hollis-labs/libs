# Build-helper vector v1

Immutable payload tree shared by Go manifest and the Node build-helper tests.
Includes binary bytes, UTF-8, CRLF, an executable file, UI/CSS and optional
hook/config/tool declarations. Hash the exact bytes without LF normalization.
The canonical `tree/plugin.yaml` was emitted by Go `manifest.Encode`; its tree
digest includes all payload files and the executable bit of `bin/native`.
Do not modify this fixture for later behavior: add a new versioned vector.
