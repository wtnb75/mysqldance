# Helper for the backlog:* tasks in Taskfile.yml.
#
# Usage: awk -v mode=<stats|next|blocked> [-v want=<ready|todo>] -f backlog.awk BACKLOG.md
#        (reads standard input when no file is given)
#
# Item headings look like: "## B-003 [todo|P2] Title". The status is todo (the
# spec is not confirmed yet), ready (the spec is confirmed; impl-backlog can run
# without asking) or done. todo and ready items are "open".
# An item may have a body line "- 依存: B-001, B-004" listing the items it
# depends on. An open item is a candidate when every dependency is in status
# done; otherwise it is "blocked". A dependency that does not exist counts as
# unmet and is reported as "(missing)".
#
#   mode=stats    print a table of item counts per priority and status
#   mode=next     print "<priority>\t<id>\t<title>" for the candidates whose
#                 status is want (ready or todo)
#   mode=blocked  print "<priority>\t<id>\t<status>\t<title>\twaiting: <ids>"
#                 for blocked open items
# next/blocked output is unsorted; the caller pipes it through sort.

/^## B-[0-9]+ \[[a-z]+\|P[0-9]\] / {
    id = $2
    meta = $3
    gsub(/[\[\]]/, "", meta)
    split(meta, m, "|")
    title = $0
    sub(/^## B-[0-9]+ \[[a-z]+\|P[0-9]\] /, "", title)
    status[id] = m[1]
    prio[id] = m[2]
    titles[id] = title
    ids[++nids] = id
    count[m[2], m[1]]++
    prios[m[2]] = 1
    next
}

# Any other heading ends the current item so that stray "依存:" lines are ignored.
/^#/ {
    id = ""
    next
}

/^- 依存:/ {
    if (id == "") {
        next
    }
    line = $0
    sub(/^- 依存:[ ]*/, "", line)
    gsub(/[, ]+/, " ", line)
    sub(/ $/, "", line)
    deps[id] = line
    next
}

# waiting() returns the unmet dependencies of item i, space separated.
function waiting(i,    n, d, k, out) {
    n = split(deps[i], d, " ")
    out = ""
    for (k = 1; k <= n; k++) {
        if (!(d[k] in status)) {
            out = out (out == "" ? "" : " ") d[k] "(missing)"
        } else if (status[d[k]] != "done") {
            out = out (out == "" ? "" : " ") d[k]
        }
    }
    return out
}

END {
    if (mode == "next" || mode == "blocked") {
        for (k = 1; k <= nids; k++) {
            i = ids[k]
            if (status[i] != "todo" && status[i] != "ready") {
                continue
            }
            w = waiting(i)
            if (mode == "next" && w == "" && status[i] == want) {
                printf "%s\t%s\t%s\n", prio[i], i, titles[i]
            } else if (mode == "blocked" && w != "") {
                printf "%s\t%s\t%s\t%s\twaiting: %s\n", prio[i], i, status[i], titles[i], w
            }
        }
        exit
    }
    if (mode != "stats") {
        exit
    }
    printf "%-9s %5s %5s %5s %6s\n", "priority", "todo", "ready", "done", "total"
    for (k = 1; k <= 9; k++) {
        p = "P" k
        if (!(p in prios)) {
            continue
        }
        t = count[p, "todo"] + 0
        r = count[p, "ready"] + 0
        f = count[p, "done"] + 0
        printf "%-9s %5d %5d %5d %6d\n", p, t, r, f, t + r + f
        tt += t
        tr += r
        tf += f
    }
    printf "%-9s %5d %5d %5d %6d\n", "total", tt, tr, tf, tt + tr + tf
}
