import type { Dispatch, SetStateAction } from "react";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Textarea } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
} from "@/shadcn-bridge/heroui/modal";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Spinner } from "@/shadcn-bridge/heroui/spinner";
import { Select, SelectItem } from "@/shadcn-bridge/heroui/select";
import { Switch } from "@/shadcn-bridge/heroui/switch";
import { type NodeInstallMode, type ReleaseChannel } from "@/api";

export interface InstallCommandModalProps {
  currentNodeName: string;
  handleManualCopy: () => Promise<void>;
  installCommand: string;
  installCommandModal: boolean;
  /** R7-J-01: explicit plaintext warning from the install API, shown before copying. */
  installWarning?: string;
  /** F03：本次轮换的新 secret（仅显示一次），关闭后无法再取。 */
  installSecret?: string;
  /** F03：在线节点未确认轮换时为 true，弹窗内出确认轮换按钮。 */
  installNeedsReissue?: boolean;
  setInstallCommandModal: Dispatch<SetStateAction<boolean>>;
  /** 安装形态（脚本 / Docker / Docker Compose）与容器调优开关。 */
  installMode: NodeInstallMode;
  installPrivileged: boolean;
  installModeLoading: boolean;
  onConfirmReissue?: () => Promise<void>;
  onCopySecret?: () => Promise<void>;
  onInstallModeChange: (mode: NodeInstallMode) => void;
  onInstallPrivilegedChange: (privileged: boolean) => void;
}

const INSTALL_MODE_OPTIONS: { key: NodeInstallMode; label: string }[] = [
  { key: "script", label: "脚本安装" },
  { key: "docker", label: "Docker" },
  { key: "docker-compose", label: "Docker Compose" },
];

const INSTALL_MODE_DESCRIPTIONS: Record<NodeInstallMode, string> = {
  script:
    "在节点服务器上下载安装脚本并以系统服务（systemd / OpenRC）方式安装 agent，配置与数据位于 /etc/ovo_agent。",
  docker:
    "节点服务器需已安装 Docker。命令把节点配置写入宿主机数据目录下的 config.json（默认 /etc/ovo_agent/config.json，多面板共存实例为 /etc/ovo_agent-<实例键>/config.json，权限 600），并以 host 网络 + NET_ADMIN 能力启动容器（默认名 ovo-agent，实例为 ovo-agent-<实例键>）；重复执行会重建该容器（升级/重装同理）。容器日志用 docker logs <容器名> 查看（容器名以命令本身为准）。",
  "docker-compose":
    "节点服务器需已安装 Docker Compose。命令把节点配置写入宿主机数据目录下的 config.json（默认 /etc/ovo_agent/config.json，多面板共存实例为 /etc/ovo_agent-<实例键>/config.json，权限 600），并把 Compose 文件写到默认 /opt/docker/ovo-agent/docker-compose.yaml（实例为 /opt/docker/ovo-agent-<实例键>/docker-compose.yaml）后启动；之后可用 docker compose -f 该文件管理。修改调优开关后重新获取命令再执行一次即可，只重写这一个 Compose 文件。",
};

/** The command that installs the agent on a node. */
export function InstallCommandModal({
  currentNodeName,
  handleManualCopy,
  installCommand,
  installCommandModal,
  installWarning,
  installSecret,
  installNeedsReissue,
  setInstallCommandModal,
  installMode,
  installPrivileged,
  installModeLoading,
  onConfirmReissue,
  onCopySecret,
  onInstallModeChange,
  onInstallPrivilegedChange,
}: InstallCommandModalProps) {
  return (
    <Modal
      backdrop="blur"
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={installCommandModal}
      placement="center"
      scrollBehavior="inside"
      size="2xl"
      onClose={() => setInstallCommandModal(false)}
    >
      <ModalContent>
        <ModalHeader>安装命令 - {currentNodeName}</ModalHeader>
        <ModalBody>
          <div className="space-y-4">
            {installWarning ? (
              <div className="rounded-xl border border-danger-500/25 bg-danger-500/10 px-3 py-2 text-sm font-medium text-danger-800">
                ⚠️ {installWarning}
              </div>
            ) : null}
            {installNeedsReissue ? (
              <div className="space-y-2 rounded-xl border border-warning-500/25 bg-warning-500/10 px-3 py-2 text-sm font-medium">
                <div>
                  该节点当前在线：重新生成安装命令将轮换节点密钥并使当前
                  agent 掉线，须用新密钥重装后才能重新上线。
                </div>
                <Button
                  color="warning"
                  size="sm"
                  variant="solid"
                  onPress={() => void onConfirmReissue?.()}
                >
                  确认轮换并重新生成
                </Button>
              </div>
            ) : null}
            {installSecret ? (
              <div className="space-y-2">
                <div className="text-sm font-medium text-foreground">
                  节点密钥（仅显示一次）
                </div>
                <div className="relative">
                  <Textarea
                    readOnly
                    className="font-mono text-sm"
                    classNames={{
                      input: "font-mono text-sm",
                    }}
                    maxRows={2}
                    minRows={1}
                    value={installSecret}
                    variant="bordered"
                  />
                  <Button
                    className="absolute top-2 right-2"
                    color="primary"
                    size="sm"
                    variant="flat"
                    onPress={() => void onCopySecret?.()}
                  >
                    复制密钥
                  </Button>
                </div>
                <p className="text-xs font-semibold text-foreground">
                  关闭弹窗后无法再取到该密钥；再次生成安装命令会轮换密钥，旧密钥立即失效。
                </p>
              </div>
            ) : null}
            <div className="space-y-2">
              <div className="text-sm font-medium text-foreground">安装方式</div>
              <div className="flex flex-wrap gap-2">
                {INSTALL_MODE_OPTIONS.map((option) => (
                  <Button
                    key={option.key}
                    color={installMode === option.key ? "primary" : "default"}
                    size="sm"
                    variant={installMode === option.key ? "solid" : "flat"}
                    onPress={() => onInstallModeChange(option.key)}
                  >
                    {option.label}
                  </Button>
                ))}
              </div>
              <p className="text-xs font-semibold text-foreground">
                {INSTALL_MODE_DESCRIPTIONS[installMode]}
              </p>
              {installMode !== "script" ? (
                <div className="flex items-center gap-3 pt-1">
                  <Switch
                    isSelected={installPrivileged}
                    size="sm"
                    onValueChange={onInstallPrivilegedChange}
                  />
                  <div className="text-xs font-semibold text-foreground">
                    <span className="font-semibold">服务器调优（特权容器）</span>
                    ：开启后容器以 --privileged 运行，面板的服务器调优
                    （BBR / sysctl / 内核模块加载）才能在容器内生效；不需要
                    调优时保持关闭即可。注意容器边界：容器内没有
                    systemd，调优中依赖 systemd 持久化的步骤（写入
                    sysctl 配置文件、开机自启类设置）在容器形态下不适用、
                    不会生效，容器重建后需重新下发调优；这类持久化调优
                    请改用脚本安装形态。
                  </div>
                </div>
              ) : null}
            </div>
            <p className="text-sm text-foreground">
              请复制以下安装命令到服务器上执行：执行时会提示输入节点密钥（不回显），请把上方「节点密钥」框里的密钥粘贴到提示处。安装命令本身不再包含密钥，粘贴后也不会留在服务器的
              shell 历史里；无终端的自动化执行请预置 OVO_NODE_SECRET
              环境变量。
            </p>
            <div className="relative">
              <Textarea
                readOnly
                className="font-mono text-sm"
                classNames={{
                  input: "font-mono text-sm",
                }}
                maxRows={10}
                minRows={6}
                value={installModeLoading ? "" : installCommand}
                variant="bordered"
              />
              {installModeLoading ? (
                <div className="absolute inset-0 flex items-center justify-center">
                  <Spinner size="sm" />
                </div>
              ) : (
                <Button
                  className="absolute top-2 right-2"
                  color="primary"
                  size="sm"
                  variant="flat"
                  onPress={handleManualCopy}
                >
                  复制
                </Button>
              )}
            </div>
            <div className="text-xs font-semibold text-foreground">
              💡 提示：如果复制按钮失效，请手动选择上方文本进行复制
            </div>
            <div className="text-xs font-semibold text-foreground">
              ✅ 执行后确认上线：命令执行完成只代表 agent
              已启动，不代表已连上面板。请回到节点列表确认该节点显示为
              <b>在线</b>；若未上线，在节点服务器执行{" "}
              {installMode === "script" ? (
                <span className="font-mono">journalctl -u ovo_agent -n 100</span>
              ) : (
                <span className="font-mono">
                  docker logs ovo-agent（多面板共存实例为 ovo-agent-&lt;实例键&gt;）
                </span>
              )}{" "}
              查看连接日志，并核对「网站配置」中面板后端地址的 scheme
              是否与面板实际对外方式一致（HTTPS 面板须为 https://）。
            </div>
          </div>
        </ModalBody>
        <ModalFooter>
          <Button variant="flat" onPress={() => setInstallCommandModal(false)}>
            关闭
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}

// R8-013：与后端 maxBatchUpgradeNodes 对齐的单批升级上限——选择面
// 提示、超限禁用确认、提交前再预检（后端仍整单校验）。
export const BATCH_UPGRADE_NODE_LIMIT = 50;

export interface UpgradeNodeModalProps {
  handleConfirmUpgrade: () => Promise<void>;
  releases: {
    version: string;
    name: string;
    publishedAt: string;
    prerelease: boolean;
    channel: ReleaseChannel;
  }[];
  releasesLoading: boolean;
  selectedIds: Set<number>;
  /** R8-013：已选中的本地节点数（远程节点提交时会被过滤，预检按此数） */
  batchLocalCount?: number;
  selectedVersion: string;
  setSelectedVersion: Dispatch<SetStateAction<string>>;
  setUpgradeModalOpen: Dispatch<SetStateAction<boolean>>;
  upgradeModalOpen: boolean;
  upgradeTarget: "single" | "batch";
}

/** Pick a version and upgrade the agent on one or more nodes. */
export function UpgradeNodeModal({
  handleConfirmUpgrade,
  releases,
  releasesLoading,
  selectedIds,
  batchLocalCount,
  selectedVersion,
  setSelectedVersion,
  setUpgradeModalOpen,
  upgradeModalOpen,
  upgradeTarget,
}: UpgradeNodeModalProps) {
  return (
    <Modal
      backdrop="blur"
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={upgradeModalOpen}
      placement="center"
      scrollBehavior="inside"
      size="md"
      onOpenChange={setUpgradeModalOpen}
    >
      <ModalContent>
        {(onClose) => (
          <>
            <ModalHeader className="flex flex-col gap-1">
              <h2 className="text-xl font-bold">
                {upgradeTarget === "batch"
                  ? `批量升级 (${selectedIds.size} 个节点)`
                  : "升级节点"}
              </h2>
            </ModalHeader>
            <ModalBody>
              {releasesLoading ? (
                <div className="flex justify-center py-8">
                  <Spinner size="lg" />
                </div>
              ) : (
                <div className="space-y-4">
                  <Select
                    label="选择版本"
                    placeholder="留空则使用最新版本"
                    selectedKeys={selectedVersion ? [selectedVersion] : []}
                    onSelectionChange={(keys) => {
                      const selected = Array.from(keys)[0] as string;

                      setSelectedVersion(selected || "");
                    }}
                  >
                    {releases.map((r) => (
                      <SelectItem key={r.version} textValue={r.version}>
                        <div className="flex justify-between items-center">
                          <span>{r.version}</span>
                          <span className="text-xs font-semibold text-foreground">
                            {r.publishedAt
                              ? new Date(r.publishedAt).toLocaleDateString()
                              : ""}
                            {r.channel === "dev" && (
                              <Chip
                                className="ml-1 shrink-0 whitespace-nowrap"
                                color="warning"
                                size="sm"
                                variant="flat"
                              >
                                测试
                              </Chip>
                            )}
                          </span>
                        </div>
                      </SelectItem>
                    ))}
                  </Select>
                  <p className="text-sm text-foreground">
                    {selectedVersion
                      ? `将升级到版本 ${selectedVersion}`
                      : "未选择版本，将自动使用最新版"}
                  </p>
                  {upgradeTarget === "batch" && (
                    <p
                      className={
                        (batchLocalCount ?? 0) > BATCH_UPGRADE_NODE_LIMIT
                          ? "text-sm font-semibold text-danger-700"
                          : "text-sm text-foreground"
                      }
                    >
                      本次将升级 {batchLocalCount ?? 0}
                      台本地节点（单次批量上限 {BATCH_UPGRADE_NODE_LIMIT} 台）
                      {(batchLocalCount ?? 0) > BATCH_UPGRADE_NODE_LIMIT
                        ? "，已超限，请减少选择后分批提交"
                        : ""}
                    </p>
                  )}
                </div>
              )}
            </ModalBody>
            <ModalFooter>
              <Button variant="light" onPress={onClose}>
                取消
              </Button>
              <Button
                color="warning"
                isDisabled={
                  releasesLoading ||
                  (upgradeTarget === "batch" &&
                    (batchLocalCount ?? 0) > BATCH_UPGRADE_NODE_LIMIT)
                }
                onPress={handleConfirmUpgrade}
              >
                确认升级
              </Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}

/** Immediate service restart: separate from the persistent schedule editor. */
export function RestartAgentModal({
  nodes,
  loading,
  onClose,
  onConfirm,
}: {
  nodes: { id: number; name: string }[] | null;
  loading: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <Modal
      backdrop="blur"
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={nodes !== null}
      placement="center"
      scrollBehavior="inside"
      size="md"
      onOpenChange={(open) => {
        if (!open && !loading) onClose();
      }}
    >
      <ModalContent>
        <ModalHeader>
          立即重启 agent
          {nodes && nodes.length > 1 ? `（${nodes.length} 台）` : ""}
        </ModalHeader>
        <ModalBody>
          <p className="text-sm">
            将重启所选节点的 agent
            服务，转发连接会短暂中断。请确认节点，重启后等待版本回报（限时 2
            分钟）。
          </p>
          <div className="max-h-48 overflow-auto text-sm">
            {nodes?.map((node) => (
              <div className="break-all" key={node.id}>
                {node.name}{" "}
                <span className="text-xs font-semibold text-muted-foreground">
                  ID {node.id}
                </span>
              </div>
            ))}
          </div>
          <p className="text-xs font-semibold text-muted-foreground">
            仅处理所选节点；每批最多 {BATCH_UPGRADE_NODE_LIMIT} 台。
          </p>
        </ModalBody>
        <ModalFooter>
          <Button variant="light" isDisabled={loading} onPress={onClose}>
            取消
          </Button>
          <Button
            color="warning"
            isLoading={loading}
            isDisabled={
              !nodes?.length || nodes.length > BATCH_UPGRADE_NODE_LIMIT
            }
            onPress={onConfirm}
          >
            确认立即重启
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
