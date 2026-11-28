import { defineConfig } from "vitepress";

export default defineConfig({
  lang: "zh-CN",
  title: "OVO",
  description: "转发 + 节点搭建面板",
  base: "/ovo/",
  cleanUrls: true,
  lastUpdated: true,
  head: [["link", { rel: "icon", type: "image/jpeg", href: "/ovo/logo.jpg" }]],
  themeConfig: {
    logo: "/logo.jpg",
    nav: [
      { text: "指南", link: "/guide/getting-started" },
      { text: "图文手册", link: "/manual/index.html" },
      { text: "在线演示", link: "https://yhfc1775-maker.github.io/ovo/demo/" },
      { text: "下载", link: "https://github.com/yhfc1775-maker/ovo/releases" },
    ],
    sidebar: [
      { text: "开始", items: [
        {text:"快速上手",link:"/guide/getting-started"},
        {text:"安装面板",link:"/guide/install#面板端"},
        {text:"安装 agent",link:"/guide/install#节点端"},
        {text:"服务器设置",link:"/guide/nodes#准备"},
        {text:"升级",link:"/guide/upgrade"},
      ]},
      { text: "节点", items: [
        {text:"内核与协议",link:"/guide/nodes#安装内核"},
        {text:"新建入站",link:"/guide/nodes#新建入站"},
        {text:"分流与链式出站",link:"/guide/nodes#出站"},
        {text:"导入节点",link:"/guide/import"},
        {text:"延迟与回程",link:"/guide/nodes#节点延迟"},
      ]},
      { text: "中转与转发", items: [
        {text:"对接中转",link:"/guide/relay"},
        {text:"隧道与转发规则",link:"/guide/forward"},
      ]},
      { text: "订阅", items: [
        {text:"订阅与客户端",link:"/guide/subscription"},
      ]},
      { text: "运维", items: [
        {text:"通知",link:"/guide/notify"},
        {text:"监控",link:"/guide/monitoring"},
        {text:"备份与恢复",link:"/guide/nodes#备份"},
        {text:"PostgreSQL",link:"/guide/postgresql"},
        {text:"通行证密钥登录",link:"/guide/passkey"},
        {text:"AI Skill",link:"/ai-skill"},
      ]},
      { text: "帮助", items: [
        {text:"常见问题",link:"/guide/faq"},
        {text:"图文手册",link:"/manual/index.html"},
        {text:"在线演示",link:"https://yhfc1775-maker.github.io/ovo/demo/"},
      ]},
    ],
    socialLinks: [{ icon: "github", link: "https://github.com/yhfc1775-maker/ovo" }],
    search: { provider: "local" },
    outline: { level: [2, 3], label: "本页目录" },
    docFooter: { prev: "上一页", next: "下一页" },
    lastUpdated: { text: "最后更新" },
    returnToTopLabel: "回到顶部",
    sidebarMenuLabel: "菜单",
    darkModeSwitchLabel: "外观",
    footer: {
      message: "基于 GPLv3 发布 · 仅供学习研究使用",
    },
  },
});
