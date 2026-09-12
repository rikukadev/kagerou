// 環境名と --env の値をそのまま返すだけ。readiness と配線の確認に使う。
export const handler = async () => ({
  statusCode: 200,
  headers: { "content-type": "application/json" },
  body: JSON.stringify({
    env: process.env.KAGEROU_ENV || "",
    greeting: process.env.GREETING || "",
  }),
});
