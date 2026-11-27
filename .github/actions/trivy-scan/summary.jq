def fixable: (.FixedVersion // "") != "";

[.Results[]?.Vulnerabilities[]?] as $v
| ($v | map(select((.Severity == "CRITICAL" or .Severity == "HIGH") and fixable)) | length) as $gate
| "### \($heading)", "",
  if ($v | length) == 0 then "No vulnerabilities found."
  else
    "| Severity | Found | Fixable |",
    "| --- | ---: | ---: |",
    (["CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"][] as $s
      | ($v | map(select(.Severity == $s))) as $m
      | "| \($s) | \($m | length) | \($m | map(select(fixable)) | length) |"),
    "| **Total** | **\($v | length)** | **\($v | map(select(fixable)) | length)** |"
  end,
  "",
  (if $gate == 0 then "Gate: pass (no fixable HIGH or CRITICAL)."
   else "Gate: **fail** (\($gate) fixable HIGH or CRITICAL)." end),
  ""
