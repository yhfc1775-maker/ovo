import type {
  InboundImportPreviewNode,
  RemoteSourceRole,
  RemoteSyncResult,
} from "@/api";
import type { InboundApiItem } from "@/api/types";

import { useEffect, useRef, useState } from "react";
import toast from "react-hot-toast";

import {
  importInbounds,
  previewInboundImport,
  saveRemoteSource,
  updateExternalInbound,
} from "@/api";
import { useIdemKey, useRotateIdemKeyOnCreateOpen } from "@/hooks/useIdemKey";
import {
  FilePick,
  INTERVALS,
  ROLE_HINTS,
  SOURCE_ROLES,
  readSubscriptionFile,
  syncText,
} from "@/pages/inbound/sources";
import {
  type ManualNode,
  ManualNodeFields,
  emptyManualNode,
  manualNodeLink,
} from "@/pages/inbound/manual-node";
import { Segmented } from "@/pages/subscription/controls";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import { Input, Textarea } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";
import { Select, SelectItem } from "@/shadcn-bridge/heroui/select";
import { Switch } from "@/shadcn-bridge/heroui/switch";

const modalBase =
  "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden";

export { EXTERNAL_LABEL } from "@/pages/inbound/group";

/** A lone http(s) URL: a subscription that can be kept in sync. */
function subscriptionURL(text: string): URL | null {
  const t = text.trim();

  if (!/^https?:\/\/\S+$/i.test(t)) return null;
  try {
    return new URL(t);
  } catch {
    return null;
  }
}

type ImportMode = "paste" | "manual";

interface ImportPreviewRow extends InboundImportPreviewNode {
  originalName: string;
  editName?: string;
  editedNameTruncated?: boolean;
  selected: boolean;
  nameEdited?: boolean;
  nameNotice?: string;
}

interface ImportPreviewState {
  fingerprint: string;
  rows: ImportPreviewRow[];
  removedIdentities: string[];
  errors: string[];
}

const IMPORT_MODES: Array<[ImportMode, string]> = [
  ["paste", "粘贴链接 / 订阅"],
  ["manual", "手动填写"],
];

export function ImportModal({
  isOpen,
  ssMethods,
  onClose,
  onImported,
  onSourceAdded,
}: {
  isOpen: boolean;
  /** SS encryption methods for the manual form (from kernel caps). */
  ssMethods: string[];
  onClose: () => void;
  onImported: () => void;
  /** A subscription URL was saved as a synced source. */
  onSourceAdded: () => void;
}) {
  const [mode, setMode] = useState<ImportMode>("paste");
  const [manual, setManual] = useState<ManualNode>(emptyManualNode);
  const [links, setLinks] = useState("");
  const [relayOnly, setRelayOnly] = useState(false);
  const [errors, setErrors] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const [sync, setSync] = useState(true);
  const [sourceName, setSourceName] = useState("");
  const [intervalHours, setIntervalHours] = useState("12");
  const [role, setRole] = useState<RemoteSourceRole>("");
  // The uploaded file the text came from, if any.
  const [fileName, setFileName] = useState("");
  const [preview, setPreview] = useState<ImportPreviewState | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const previewRequest = useRef(0);

  useEffect(() => {
    if (isOpen) {
      setMode("paste");
      setManual(emptyManualNode());
      setLinks("");
      setRelayOnly(false);
      setErrors([]);
      setSync(true);
      setSourceName("");
      setIntervalHours("12");
      setRole("");
      setFileName("");
      setPreview(null);
      setPreviewLoading(false);
      previewRequest.current += 1;
    } else {
      previewRequest.current += 1;
      setPreviewLoading(false);
    }
  }, [isOpen]);

  const manualMode = mode === "manual";
  const subURL = manualMode ? null : subscriptionURL(links);
  const fromFile = !manualMode && Boolean(fileName) && !subURL;
  const syncing = (Boolean(subURL) || fromFile) && sync;

  // R2X-004：订阅源新建表单幂等键（同表单复用、成功轮换）。
  const { idemKey, rotateIdemKey } = useIdemKey();

  // R3X-027：创建弹窗打开（上升沿且新建模式）时轮换幂等键；窗内重试仍复用同键。
  useRotateIdemKeyOnCreateOpen(isOpen, true, rotateIdemKey);

  const addSource = async () => {
    if ((!subURL && !fromFile) || !preview) return;
    if (!preview.rows.some((row) => row.selected)) {
      toast.error("请至少选择一个节点");
      return;
    }
    setSaving(true);
    try {
      const nameOverrides: Record<string, string> = {};
      const excludedIdentities: string[] = [...preview.removedIdentities];
      for (const row of preview.rows) {
        if (!row.selected || row.alreadyImported) {
          excludedIdentities.push(row.identityKey);
          continue;
        }
        if (row.nameEdited || row.nameDuplicated) {
          nameOverrides[row.identityKey] = row.name.trim() || row.originalName;
        }
      }
      const res = await saveRemoteSource(
        subURL
          ? {
              name: sourceName.trim() || subURL.hostname,
              url: subURL.toString(),
              intervalHours: Number(intervalHours),
              relayOnly,
              role,
              nameOverrides,
              excludedIdentities,
              previewFingerprint: preview.fingerprint,
            }
          : {
              name: sourceName.trim() || fileName.replace(/\.[^.]+$/, ""),
              url: "",
              kind: "file",
              content: links,
              intervalHours: 0,
              relayOnly,
              role,
              nameOverrides,
              excludedIdentities,
              previewFingerprint: preview.fingerprint,
            },
        idemKey,
      );

      if (res.code === 0 || res.data || res.msg?.includes("订阅内容已变化")) {
        rotateIdemKey();
      }
      const result = (res.data as { result?: RemoteSyncResult } | null)?.result;

      // A source whose first fetch failed is still saved.
      if (res.code !== 0 && !res.data) {
        toast.error(res.msg || "添加失败");

        return;
      }
      if (res.code !== 0)
        toast(res.msg || "已保存，但更新失败", { icon: "⚠️" });
      // R3X-025：首同步带逐节点失败明细（result.errors）时按 ⚠️ 呈现，
      // 明细条数 + 首条已在 syncText 内带出，不得报全成功。
      else if (result?.errors?.length)
        toast(`已添加：${syncText(result)}`, { icon: "⚠️", duration: 5000 });
      else toast.success(result ? `已添加：${syncText(result)}` : "已添加");
      // R2-02（防静默）：纯中转源回执的 warning（其导入节点不可订阅）
      // 同款透出，不得吞掉。
      if (res.warning) {
        toast(res.warning, { icon: "⚠️", duration: 12000 });
      }
      onSourceAdded();
      onClose();
    } finally {
      setSaving(false);
    }
  };

  const parsePreview = async (rawText = links) => {
    const requestText = rawText.trim();

    if (!requestText) return;
    const requestID = ++previewRequest.current;
    setPreviewLoading(true);
    setErrors([]);
    try {
      const res = await previewInboundImport(requestText);
      if (requestID !== previewRequest.current) return;
      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "解析失败");
        return;
      }
      const rows: ImportPreviewRow[] = res.data.nodes.map((node) => ({
        ...node,
        originalName: node.name,
        editName: node.name,
        selected: true,
        nameNotice: [
          node.nameTruncated ? "原名称超过 100 字，已截断" : "",
          node.nameDuplicated ? "名称重名，已自动添加后缀" : "",
        ]
          .filter(Boolean)
          .join("；") || undefined,
      }));
      setPreview({
        fingerprint: res.data.fingerprint,
        rows,
        removedIdentities: [],
        errors: res.data.errors ?? [],
      });
    } catch {
      if (requestID === previewRequest.current) {
        toast.error("解析失败，请检查内容后重试");
      }
    } finally {
      if (requestID === previewRequest.current) setPreviewLoading(false);
    }
  };

  const updatePreviewName = (identityKey: string, rawName: string) => {
    if (!preview) return;
    const codepoints = Array.from(rawName.trim());
    const tooLong = codepoints.length > 100;
    const requested = codepoints.slice(0, 100).join("");
    const rows = preview.rows.map((row) => ({
      ...row,
      name:
        row.identityKey === identityKey
          ? requested
          : row.nameEdited
            ? row.name
            : row.originalName,
      editName: row.identityKey === identityKey ? requested : row.editName,
      editedNameTruncated: row.identityKey === identityKey ? tooLong : row.editedNameTruncated,
      nameEdited: row.identityKey === identityKey ? true : row.nameEdited,
    }));
    const used = new Set<string>();
    const normalized = rows.map((row) => {
      const base =
        (row.nameEdited ? row.editName : row.originalName)?.trim() ||
        row.originalName;
      let name = base;
      let suffix = 2;
      while (used.has(name.toLowerCase())) {
        const tail = ` (${suffix++})`;
        name = `${Array.from(base).slice(0, 100 - Array.from(tail).length).join("")}${tail}`;
      }
      used.add(name.toLowerCase());
      const notices: string[] = [];
      if (row.nameEdited ? row.editedNameTruncated : row.nameTruncated) {
        notices.push("名称超过 100 字，已截断");
      }
      if ((!row.nameEdited && row.nameDuplicated) || name !== base) {
        notices.push("名称重名，已自动添加后缀");
      }
      const nameNotice = notices.length > 0 ? notices.join("；") : undefined;

      return { ...row, name, nameNotice };
    });
    setPreview({ ...preview, rows: normalized });
  };

  const submit = async () => {
    if (!preview) {
      if (manualMode) {
        const built = manualNodeLink(manual);

        if (!built.link) {
          setErrors([]);
          toast.error(built.error ?? "请检查填写的内容");
          return;
        }
        setLinks(built.link);
        await parsePreview(built.link);
      } else {
        await parsePreview();
      }
      return;
    }
    if (syncing) {
      await addSource();

      return;
    }
    setSaving(true);
    try {
      const selectedRows =
        preview?.rows.filter((row) => row.selected && !row.alreadyImported) ??
        [];
      const res = await importInbounds(
        links,
        relayOnly,
        {
          fingerprint: preview.fingerprint,
          selected: selectedRows.map((row) => row.identityKey),
          nameOverrides: Object.fromEntries(
            selectedRows
              .filter((row) => row.nameEdited || row.nameDuplicated)
              .map((row) => [
                row.identityKey,
                row.name.trim() || row.originalName,
              ]),
          ),
        },
      );
      const failed = res.data?.errors ?? [];

      setErrors(failed);
      if (res.code !== 0) {
        if (failed.length === 0) toast.error(res.msg || "导入失败");

        return;
      }
      const created = res.data?.created.length ?? 0;

      onImported();
      if (created > 0) {
        const warnings = res.data?.warnings ?? [];
        const notices: string[] = [];
        if (failed.length > 0) notices.push(`另有 ${failed.length} 条未导入`);
        if (warnings.length > 0) {
          notices.push(`${warnings.length} 个名称重名，已自动添加后缀`);
        }
        if (notices.length > 0) {
          toast(`已导入 ${created} 个；${notices.join("；")}`, {
            icon: "⚠️",
            duration: 6000,
          });
        } else {
          toast.success(manualMode ? "已添加节点" : `已导入 ${created} 个节点`);
        }
        onClose();
      } else {
        toast(`没有导入节点：${failed.length} 个失败或重复`, { icon: "⚠️" });
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      backdrop="blur"
      classNames={{ base: modalBase }}
      isOpen={isOpen}
      placement="center"
      scrollBehavior="inside"
      size="lg"
      onOpenChange={(open) => !open && onClose()}
    >
      <ModalContent>
        <ModalHeader className="flex flex-col gap-1">
          <h2 className="text-lg font-bold">导入节点</h2>
          <p className="text-small font-normal text-foreground">
            导入别处搭好的节点或机场订阅，用来对接中转和加入订阅。面板不会部署或修改这些节点。
          </p>
        </ModalHeader>
        <ModalBody>
          <Segmented
            options={IMPORT_MODES}
            value={mode}
            onChange={(m) => {
              previewRequest.current += 1;
              setPreviewLoading(false);
              setMode(m);
              setErrors([]);
              setPreview(null);
            }}
          />
          {manualMode && !preview ? (
            <ManualNodeFields
              ssMethods={ssMethods}
              value={manual}
              onChange={setManual}
            />
          ) : (
            <>
              {!preview ? (
                <>
                  <Textarea
                    description="支持：分享链接（ss / vmess / vless / trojan / hysteria2 / tuic / anytls / mierus / naive / socks5，每行一个；naive 接受 naive:// 与 naive+https:// 两种写法）、裸格式 地址:端口:用户名:密码（SOCKS5）、base64 订阅、Clash / Mihomo 配置、sing-box 配置、Surge / Loon 节点行，或直接填订阅地址（https://...）。Hysteria2 / TUIC / AnyTLS 作链式中转目标时只能走 sing-box 内核，Xray 会拒绝。"
                    label="节点或订阅"
                    minRows={5}
                    placeholder={"vless://...\nss://...\n203.0.113.10:1080:用户名:密码\n或 https://订阅地址"}
                    value={links}
                    variant="bordered"
                    onChange={(e) => {
                      previewRequest.current += 1;
                      setPreviewLoading(false);
                      setLinks(e.target.value);
                      setFileName("");
                      setPreview(null);
                    }}
                  />
                  <FilePick
                    fileName={fileName}
                    onFile={async (file) => {
                      const text = await readSubscriptionFile(file);

                      if (text !== null) {
                        previewRequest.current += 1;
                        setPreviewLoading(false);
                        setLinks(text);
                        setFileName(file.name);
                        setPreview(null);
                      }
                    }}
                  />
                </>
              ) : (
                <div className="space-y-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <p className="text-sm font-semibold">
                      预览 {preview.rows.length} 个节点；确认后才会导入
                    </p>
                    <div className="flex gap-2">
                      <Button
                        size="sm"
                        variant="flat"
                        onPress={() =>
                          setPreview({
                            ...preview,
                            rows: preview.rows.map((row) => ({ ...row, selected: true })),
                          })
                        }
                      >
                        全选
                      </Button>
                      <Button
                        size="sm"
                        variant="flat"
                        onPress={() =>
                          setPreview({
                            ...preview,
                            rows: preview.rows.map((row) => ({ ...row, selected: false })),
                          })
                        }
                      >
                        取消全选
                      </Button>
                      <Button size="sm" variant="light" onPress={() => setPreview(null)}>
                        返回编辑
                      </Button>
                    </div>
                  </div>
                  {(subURL || fromFile) && (
                    <div className="flex items-start gap-2 rounded-xl border border-divider p-3">
                      <Switch isSelected={sync} size="sm" onValueChange={setSync} />
                      <div>
                        <p className="text-sm">{subURL ? "定时同步" : "存为订阅源"}</p>
                        <p className="text-xs font-semibold text-foreground">
                          {!sync
                            ? "只导入本次选中的节点，之后不再更新"
                            : subURL
                              ? "存为订阅源：按时重新拉取，节点增删改自动跟上"
                              : "以后重新上传文件，节点增删改自动跟上"}
                        </p>
                      </div>
                    </div>
                  )}
                  {preview.errors.length > 0 && (
                    <div className="space-y-1 rounded-xl bg-danger-500/10 p-3 text-xs font-semibold text-danger-700">
                      <p className="font-semibold">以下内容解析失败，其他有效节点仍可导入：</p>
                      {preview.errors.map((error, index) => (
                        <p key={`${index}-${error}`} className="break-all">{error}</p>
                      ))}
                    </div>
                  )}
                  <div className="max-h-[45vh] space-y-2 overflow-y-auto">
                    {preview.rows.map((row) => (
                      <div key={row.identityKey} className="rounded-xl border border-divider p-3">
                        <div className="flex items-start gap-2">
                          <Checkbox
                            isSelected={row.selected}
                            onValueChange={(selected) =>
                              setPreview({
                                ...preview,
                                rows: preview.rows.map((item) =>
                                  item.identityKey === row.identityKey
                                    ? { ...item, selected }
                                    : item,
                                ),
                              })
                            }
                          />
                          <div className="min-w-0 flex-1 space-y-2">
                            <div className="grid grid-cols-1 gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                              <Input
                                aria-label={`节点 ${row.address}:${row.port} 名称`}
                                label="名称"
                                size="sm"
                                value={row.editName ?? row.name}
                                variant="bordered"
                                onChange={(event) => updatePreviewName(row.identityKey, event.target.value)}
                              />
                              <p className="self-end break-all text-xs font-semibold text-foreground">
                                {row.protocol.toUpperCase()} · {row.address.includes(":") ? `[${row.address}]` : row.address}:{row.port}
                              </p>
                            </div>
                            <p className="text-xs font-semibold">{row.description}</p>
                            {row.params && row.params.length > 0 && (
                              <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs font-semibold">
                                {row.params.map(([key, value]) => (
                                  <span key={`${key}-${value}`} className="break-all">
                                    {key}: {value}
                                  </span>
                                ))}
                              </div>
                            )}
                            {(row.nameNotice || row.alreadyImported) && (
                              <p className="text-xs font-semibold text-warning-700">
                                {[row.nameNotice, row.alreadyImported
                                  ? syncing
                                    ? "已存在相同节点，存为订阅源时不会重复导入"
                                    : "已导入过，确认时会跳过重复节点"
                                  : ""]
                                  .filter(Boolean)
                                  .join("；")}
                              </p>
                            )}
                          </div>
                          <Button
                            color="danger"
                            size="sm"
                            variant="light"
                            onPress={() =>
                              setPreview({
                                ...preview,
                                rows: preview.rows.filter((item) => item.identityKey !== row.identityKey),
                                removedIdentities: [...new Set([...preview.removedIdentities, row.identityKey])],
                              })
                            }
                          >
                            删除
                          </Button>
                        </div>
                      </div>
                    ))}
                    {preview.rows.length === 0 && (
                      <p className="rounded-xl border border-divider p-4 text-center text-sm">
                        没有可导入节点
                      </p>
                    )}
                  </div>
                  {syncing && (
                    <div className="space-y-3 rounded-xl border border-divider p-3">
                      <Input
                        label="订阅源名称"
                        placeholder={subURL ? subURL.hostname : fileName.replace(/\.[^.]+$/, "")}
                        size="sm"
                        value={sourceName}
                        variant="bordered"
                        onChange={(e) => setSourceName(e.target.value)}
                      />
                      {subURL && (
                        <Segmented options={INTERVALS} value={intervalHours} onChange={setIntervalHours} />
                      )}
                      <div className="space-y-1.5">
                        <p className="text-sm">用途</p>
                        <Segmented options={SOURCE_ROLES} value={role} onChange={(v) => setRole(v as RemoteSourceRole)} />
                        <p className="text-xs font-semibold text-foreground">{ROLE_HINTS[role]}</p>
                      </div>
                    </div>
                  )}
                </div>
              )}
              {!preview && (subURL || fromFile) && (
                <div className="space-y-3 rounded-xl border border-divider p-3">
                  <div className="flex items-start gap-2">
                    <Switch
                      isSelected={sync}
                      size="sm"
                      onValueChange={setSync}
                    />
                    <div>
                      <p className="text-sm">
                        {subURL ? "定时同步" : "存为订阅源"}
                      </p>
                      <p className="text-xs font-semibold text-foreground">
                        {!sync
                          ? "只导入一次，之后不再更新"
                          : subURL
                            ? "存为订阅源：按时重新拉取，节点增删改自动跟上"
                            : "以后重新上传文件，节点增删改自动跟上"}
                      </p>
                    </div>
                  </div>
                  {sync && (
                    <>
                      <Input
                        label="名称"
                        placeholder={
                          subURL
                            ? subURL.hostname
                            : fileName.replace(/\.[^.]+$/, "")
                        }
                        size="sm"
                        value={sourceName}
                        variant="bordered"
                        onChange={(e) => setSourceName(e.target.value)}
                      />
                      {subURL && (
                        <Segmented
                          options={INTERVALS}
                          value={intervalHours}
                          onChange={setIntervalHours}
                        />
                      )}
                      <div className="space-y-1.5">
                        <p className="text-sm">用途</p>
                        <Segmented
                          options={SOURCE_ROLES}
                          value={role}
                          onChange={(v) => setRole(v as RemoteSourceRole)}
                        />
                        <p className="text-xs font-semibold text-foreground">
                          {ROLE_HINTS[role]}
                        </p>
                      </div>
                    </>
                  )}
                </div>
              )}
            </>
          )}
          <Checkbox isSelected={relayOnly} onValueChange={setRelayOnly}>
            <span className="text-sm">
              只通过我的转发访问（订阅里不放直连，只放中转线路）
            </span>
          </Checkbox>
          {errors.length > 0 && (
            <ul className="space-y-1 rounded-xl bg-danger-500/10 p-3 text-xs font-semibold text-danger-700">
              {errors.map((e, idx) => (
                <li key={idx} className="break-all">
                  {e}
                </li>
              ))}
            </ul>
          )}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" onPress={onClose}>
            取消
          </Button>
          <Button
            color="primary"
            isDisabled={
              (!manualMode && !links.trim()) ||
              Boolean(
                preview &&
                  !preview.rows.some((row) => row.selected && !row.alreadyImported),
              )
            }
            isLoading={saving || previewLoading}
            onPress={submit}
          >
            {!preview
              ? previewLoading
                ? "解析中…"
                : "解析预览"
              : syncing
                ? fromFile
                  ? "确认添加订阅源"
                  : "确认添加并同步"
                : manualMode
                  ? "确认添加"
                  : "确认导入"}
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}

interface EditForm {
  name: string;
  host: string;
  port: string;
  method: string;
  password: string;
  link: string;
  remark: string;
  relayOnly: boolean;
}

export function ExternalEditModal({
  item,
  ssMethods,
  onClose,
  onSaved,
}: {
  item: InboundApiItem | null;
  ssMethods: string[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [form, setForm] = useState<EditForm | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setForm(
      item
        ? {
            name: item.name,
            host: item.listen,
            port: String(item.port),
            method: item.settings.method ?? "",
            password: item.settings.password ?? "",
            link: "",
            remark: item.remark,
            relayOnly: item.relayOnly,
          }
        : null,
    );
  }, [item]);

  // Nodes imported before other protocols were supported keep plain ss
  // fields; newer ones store the full proxy and are replaced by a link.
  const legacy = Boolean(item && !item.importedSummary);

  const set = (patch: Partial<EditForm>) =>
    setForm((p) => (p ? { ...p, ...patch } : p));

  const submit = async () => {
    if (!item || !form) return;
    // 提交前校验（此前零校验，空名/空 host/NaN 端口直接上送被后端笼统打回）
    if (!form.name.trim()) {
      toast.error("名称不能为空");

      return;
    }
    if (!form.host.trim()) {
      toast.error("地址不能为空");

      return;
    }
    const port = Number(form.port);

    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      toast.error("端口必须在 1-65535 之间");

      return;
    }
    setSaving(true);
    try {
      const res = await updateExternalInbound({
        id: item.id,
        name: form.name,
        host: form.host.trim(),
        port: Number(form.port),
        ...(legacy
          ? { method: form.method, password: form.password }
          : { link: form.link.trim() }),
        remark: form.remark,
        relayOnly: form.relayOnly,
      });

      if (res.code !== 0) {
        toast.error(res.msg || "保存失败");

        return;
      }
      if (res.msg && res.msg !== "操作成功") {
        toast(res.msg, { icon: "⚠️" });
      } else {
        toast.success("已保存");
      }
      onSaved();
      onClose();
    } finally {
      setSaving(false);
    }
  };

  const methods =
    form?.method && !ssMethods.includes(form.method)
      ? [...ssMethods, form.method]
      : ssMethods;

  return (
    <Modal
      backdrop="blur"
      classNames={{ base: modalBase }}
      isOpen={Boolean(item)}
      placement="center"
      scrollBehavior="inside"
      size="lg"
      onOpenChange={(open) => !open && onClose()}
    >
      <ModalContent>
        <ModalHeader className="flex flex-col gap-1">
          <h2 className="text-lg font-bold">编辑导入的节点</h2>
          <p className="text-small font-normal text-foreground">
            只修改面板里的记录；原服务器上的配置变了才需要在这里同步
          </p>
        </ModalHeader>
        <ModalBody>
          {form && (
            <div className="space-y-3">
              <Input
                label="名称"
                value={form.name}
                variant="bordered"
                onChange={(e) => set({ name: e.target.value })}
              />
              <div className="grid grid-cols-3 gap-3">
                <div className="col-span-2">
                  <Input
                    label="地址"
                    value={form.host}
                    variant="bordered"
                    onChange={(e) => set({ host: e.target.value })}
                  />
                </div>
                <Input
                  label="端口"
                  type="number"
                  value={form.port}
                  variant="bordered"
                  onChange={(e) => set({ port: e.target.value })}
                />
              </div>
              {legacy ? (
                <>
                  <Select
                    label="加密方式"
                    selectedKeys={form.method ? [form.method] : []}
                    onSelectionChange={(keys) =>
                      set({ method: String(Array.from(keys)[0] ?? "") })
                    }
                  >
                    {methods.map((m) => (
                      <SelectItem key={m} textValue={m}>
                        {m}
                      </SelectItem>
                    ))}
                  </Select>
                  <Input
                    label="密码"
                    value={form.password}
                    variant="bordered"
                    onChange={(e) => set({ password: e.target.value })}
                  />
                </>
              ) : (
                <Textarea
                  description={`当前：${item?.importedSummary ?? ""}。原节点的密码、密钥等参数变了时，粘贴新的分享链接或节点行替换；留空保持不变`}
                  label="用新链接替换参数（可选）"
                  minRows={2}
                  placeholder="vless://... / ss://..."
                  value={form.link}
                  variant="bordered"
                  onChange={(e) => set({ link: e.target.value })}
                />
              )}
              <Input
                label="备注"
                value={form.remark}
                variant="bordered"
                onChange={(e) => set({ remark: e.target.value })}
              />
              <Checkbox
                isSelected={form.relayOnly}
                onValueChange={(v) => set({ relayOnly: v })}
              >
                <span className="text-sm">
                  只通过我的转发访问（订阅里不放直连，只放中转线路）
                </span>
              </Checkbox>
            </div>
          )}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" onPress={onClose}>
            取消
          </Button>
          <Button color="primary" isLoading={saving} onPress={submit}>
            保存
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
