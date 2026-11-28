import { Select, SelectItem } from "@/shadcn-bridge/heroui/select";
import { setAppearance, useAppearance } from "@/utils/appearance";

export function AppearanceSettings() {
  const appearance = useAppearance();
  return (
    <Select
      label="界面主题"
      description="仅影响当前浏览器；两种主题均保持浅色。"
      selectedKeys={[appearance]}
      onSelectionChange={(keys) =>
        setAppearance(keys.has("flyx") ? "flyx" : "default")
      }
    >
      <SelectItem key="default">默认主题</SelectItem>
      <SelectItem key="flyx">flyx 主题</SelectItem>
    </Select>
  );
}
