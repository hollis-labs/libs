# Tool annotation compatibility fixtures

`without-annotations.json` is the pre-annotation schema-2 shape. The new
SDK must still decode it. `with-annotations.json` covers read, write and
destructive declarations with explicit hints.

`older-strict-host-error.txt` freezes the diagnostic observed from the
unchanged decoder at commit 7165988c66cb1d8d960d56ce4307228405494fd7 when
reading `with-annotations.json`: `manifest: unknown field "annotations"`.
It documents version skew; tests do not depend on building or simulating old
SDK code. Hosts using that strict decoder refuse the declaration, rather than
ignoring hints. Use the SDK release carrying annotations before consuming
manifests that include them; no release number is assigned by this change.
