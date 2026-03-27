#!/usr/bin/env node

const childProcess = require('child_process');

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

childProcess.execFileSync(resolveBinaryPath(), process.argv.slice(2), {
    stdio: 'inherit',
});
