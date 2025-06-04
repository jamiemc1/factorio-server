trap 'handle_term' TERM INT

handle_term()
{
    echo "Doing something else"
	kill -TERM ${pid}
}

pid=$(ps aux | grep "spotify" | awk '{print $2}')
#tail --pid=${active_pid} -f /dev/null
#lsof -p $pid +r 1m%s -t | grep -qm1 $(date -v+${timeout}S +%s 2>/dev/null || echo INF)
until kill -s 0 "$pid" 2>/dev/null; do sleep 1; done


trap - TERM INT


