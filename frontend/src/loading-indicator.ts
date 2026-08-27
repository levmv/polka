let activeLoads = 0;

export function beginGlobalLoading(): () => void {
    activeLoads++;
    document.body.classList.add('busy');

    let done = false;
    return () => {
        if (done) return;
        done = true;
        activeLoads = Math.max(0, activeLoads - 1);
        if (activeLoads === 0) document.body.classList.remove('busy');
    };
}
