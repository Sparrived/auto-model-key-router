package selfupdate

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// Apply 执行一次完整的自更新：下载 → 校验 → 暂存 → 就地替换。
//
// 返回被挪开的旧文件路径（stale），调用方须在旧进程退出后删除它——见 StaleSuffix。
// 之所以要把 stale 交出去而不是在这里删掉：Windows 不允许删除正在运行的映像，
// 本进程（或另一个仍在跑的旧进程）正是那个映像的持有者。
//
// 校验失败时**绝不安装**：宁可不更新，也不能把用户换成一个来源不可信的可执行文件。
// 下载与校验都发生在暂存之前，因此任何一步失败都还没动过已安装的二进制。
//
// 产物与校验和两个地址都由 version/asset 推出（而不是由调用方传 url）：调用方本就不该
// 关心下载源。真正的下载源在更下一层决定（fetchWithRetry 的候选回退，见 selfupdate.go）：
// 产物与校验和**各自独立**地先试直连、再试镜像，因此两者的内容始终来自同一个 GitHub
// Release——不一致只会表现为校验失败（拒绝安装），不会装上错误的东西。
func Apply(client *http.Client, version, asset, tempDir, executable string) (string, error) {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	targetDir := filepath.Dir(executable)

	// 下载物放临时目录：安装目录可能不可写（例如安装在 Program Files），而且下载到
	// 一半失败时不该在安装目录留下垃圾。
	downloaded := filepath.Join(tempDir, asset)
	defer func() { _ = os.Remove(downloaded) }()

	if err := Download(client, DownloadURL(version, asset), downloaded); err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	digest, err := FetchChecksums(client, version, asset)
	if err != nil {
		return "", fmt.Errorf("获取校验和失败: %w", err)
	}
	if err := VerifySHA256(downloaded, digest); err != nil {
		return "", err
	}
	return Install(downloaded, targetDir, executable)
}

// Install 把已下载并**已校验**的文件装到 executable 的位置，返回旧文件路径。
//
// 与 Apply 分开是为了让「下载/校验」与「替换」各自可测：替换这一步只需要两个本地
// 文件，不需要网络。
func Install(downloaded, targetDir, executable string) (string, error) {
	staged, err := Stage(downloaded, targetDir)
	if err != nil {
		return "", fmt.Errorf("暂存新版本失败: %w", err)
	}
	stale, err := Replace(executable, staged)
	if err != nil {
		// 暂存文件不再有用（替换失败已回滚），清掉免得留下 amkr-new.exe。
		_ = os.Remove(staged)
		return "", err
	}
	return stale, nil
}
