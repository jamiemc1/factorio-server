#!/bin/bash

prep_term()
{
    unset term_child_pid
    unset term_kill_needed
    trap 'handle_term' TERM INT
}

handle_term()
{
    if [ "${term_child_pid}" ]; then
        kill -TERM "${term_child_pid}" 2>/dev/null
    else
        term_kill_needed="yes"
    fi
}

wait_term()
{
  term_child_pid=$(ps aux | grep "warp" | awk "{print $2}")
  if [ "${term_kill_needed}" ]; then
	  kill -TERM "${term_child_pid}" 2>/dev/null 
  fi
  wait ${term_child_pid} 2>/dev/null
  trap - TERM INT
  wait ${term_child_pid} 2>/dev/null
}

# EXAMPLE USAGE
prep_term
echo Prepped
sleep 300 &
echo Waiting
wait_term
