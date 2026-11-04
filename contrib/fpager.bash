# fpager: notify the Flipper about long-running interactive bash commands.
# Source from ~/.bashrc:  . /path/to/flipper-notify/contrib/fpager.bash
# FPAGER_MIN      minimum duration in seconds (default 60)
# FPAGER_IGNORE   space-separated command names never reported (interactive tools)

: "${FPAGER_MIN:=60}"
: "${FPAGER_IGNORE:=vim nvim vi nano less more man ssh mosh htop top btop tmux screen claude watch tail journalctl}"

# PS0 is expanded in the current shell right before a command runs; the arithmetic
# side effect records the start time without a DEBUG trap.
PS0="\${PS1:\$((__fpager_start=SECONDS, 0)):0}${PS0}"

__fpager_precmd() {
	local code=$?
	if [[ -n ${__fpager_start-} ]]; then
		local d=$((SECONDS - __fpager_start)) line name
		unset __fpager_start
		if ((d >= FPAGER_MIN)); then
			line=$(HISTTIMEFORMAT='' builtin history 1)
			line=${line#*[0-9]  }
			name=${line%% *}
			name=${name##*/}
			if [[ " $FPAGER_IGNORE " != *" $name "* ]]; then
				local lvl=ok
				((code != 0)) && lvl=fail
				(fpager send -t "$name" -l "$lvl" "exit $code in ${d}s
\$ $line
$PWD" >/dev/null 2>&1 &)
			fi
		fi
	fi
	return $code
}

if [[ ";${PROMPT_COMMAND-};" != *";__fpager_precmd;"* ]]; then
	PROMPT_COMMAND="__fpager_precmd${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
