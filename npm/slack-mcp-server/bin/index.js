#!/usr/bin/env node

const childProcess = require('child_process');
const os = require('os');

const BINARY_MAP = {
    darwin_x64: {name: '@aaronsb/slack-mcp-darwin-amd64', bin: 'slack-mcp-darwin-amd64'},
    darwin_arm64: {name: '@aaronsb/slack-mcp-darwin-arm64', bin: 'slack-mcp-darwin-arm64'},
    linux_x64: {name: '@aaronsb/slack-mcp-linux-amd64', bin: 'slack-mcp-linux-amd64'},
    linux_arm64: {name: '@aaronsb/slack-mcp-linux-arm64', bin: 'slack-mcp-linux-arm64'},
    win32_x64: {name: '@aaronsb/slack-mcp-windows-amd64', bin: 'slack-mcp-windows-amd64.exe'},
    win32_arm64: {name: '@aaronsb/slack-mcp-windows-arm64', bin: 'slack-mcp-windows-arm64.exe'},
};

const resolveBinaryPath = () => {
    try {
        const binary = BINARY_MAP[`${process.platform}_${process.arch}`];
        return require.resolve(`${binary.name}/bin/${binary.bin}`);
    } catch (e) {
        throw new Error(`Could not resolve binary path for platform/arch: ${process.platform}/${process.arch}`);
    }
};

// spawn, not execFileSync: a synchronous child never hears the signals sent
// to this wrapper, so killing the wrapper orphaned the server with the pipe
// still open (#82). Forward them, and exit the way the server did.
const child = childProcess.spawn(resolveBinaryPath(), process.argv.slice(2), {
    stdio: 'inherit',
});

for (const signal of ['SIGTERM', 'SIGINT', 'SIGHUP']) {
    process.on(signal, () => {
        // On win32 only SIGINT/SIGTERM/SIGKILL/SIGQUIT are killable; kill()
        // throws ENOSYS for SIGHUP (console close), so map it to SIGTERM.
        const forward = process.platform === 'win32' && signal === 'SIGHUP' ? 'SIGTERM' : signal;
        try {
            child.kill(forward);
        } catch (err) {
            console.error(`slack-mcp: could not forward ${signal}: ${err.message}`);
        }
    });
}

child.on('error', (err) => {
    console.error(`slack-mcp: ${err.message}`);
    process.exit(1);
});

child.on('exit', (code, signal) => {
    if (signal) {
        process.exit(128 + (os.constants.signals[signal] || 0));
    }
    process.exit(code ?? 1);
});
