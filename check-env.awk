# Compares variable names between .env.example and .env, in that argument
# order. Run by `make check-env`, and by `make deploy` after the pull.
#
# Names only. The binary validates its own values at startup and reports them
# clearly, so repeating that here would only add a second thing to keep in
# step. What the binary cannot check is a name it never receives:
# TEENYURL_NETWORK is read by Compose alone, and a missing one fails the
# deploy rather than the process.
#
# In .env.example an uncommented name is required and a commented one is a
# documented optional, which is why the optionals carry their defaults.

FNR == NR {
    if ($0 ~ /^[A-Za-z_][A-Za-z0-9_]*=/) {
        k = substr($0, 1, index($0, "=") - 1)
        req[++nr] = k
        known[k] = 1
    } else if ($0 ~ /^#[ \t]*[A-Za-z_][A-Za-z0-9_]*=/) {
        sub(/^#[ \t]*/, "")
        known[substr($0, 1, index($0, "=") - 1)] = 1
    }
    next
}

$0 ~ /^[A-Za-z_][A-Za-z0-9_]*=/ {
    k = substr($0, 1, index($0, "=") - 1)
    set[k] = 1
    # An empty value is the same as unset to the binary, so treat it as unset.
    if (substr($0, index($0, "=") + 1) == "") blank[k] = 1
    if (!known[k]) extra[++nx] = k
}

END {
    for (i = 1; i <= nr; i++)
        if (!set[req[i]])       { print "missing from .env:   " req[i]; bad++ }
        else if (blank[req[i]]) { print "empty in .env:       " req[i]; bad++ }

    # Reported, never fatal: the file is shared with Compose, which has
    # variables of its own that this project has no business documenting.
    for (i = 1; i <= nx; i++)
        print "not in .env.example: " extra[i] " (undocumented, ignored)"

    if (bad) {
        print ""
        print bad " variable(s) to fix before deploying"
        exit 1
    }
    print ".env covers every variable in .env.example"
}
