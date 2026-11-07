export const PUBLIC_REGIONS: { id: string; name: string }[] = [
  { id: "ap-guangzhou", name: "广州" },
  { id: "ap-shanghai", name: "上海" },
  { id: "ap-nanjing", name: "南京" },
  { id: "ap-beijing", name: "北京" },
  { id: "ap-chengdu", name: "成都" },
  { id: "ap-chongqing", name: "重庆" },
  { id: "ap-hongkong", name: "中国香港" },
  { id: "ap-singapore", name: "新加坡" },
  { id: "ap-tokyo", name: "东京" },
  { id: "ap-seoul", name: "首尔" },
  { id: "ap-bangkok", name: "曼谷" },
  { id: "ap-jakarta", name: "雅加达" },
  { id: "ap-mumbai", name: "孟买" },
  { id: "na-ashburn", name: "弗吉尼亚" },
  { id: "na-siliconvalley", name: "硅谷" },
  { id: "eu-frankfurt", name: "法兰克福" },
  { id: "sa-saopaulo", name: "圣保罗" },
];

export function resolveRegions(remote?: { id: string; name: string }[]): { id: string; name: string }[] {
  return remote?.length ? remote : PUBLIC_REGIONS;
}
