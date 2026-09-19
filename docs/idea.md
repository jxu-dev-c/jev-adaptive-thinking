目标：做一个cliproxyapi plugin使用jev来根据用户session首个prompt, 自动路由当前session的模型, 同一个session模型第一次调整后不会再调整

路由的目的是优化成本并且提高速度

仅在请求本身是指向“jev-auto”模型时生效

当前列入考虑的路由模型id(可调整的json config):
`deepseek-v4-flash:deepseek`
`gpt-5.6-sol`
`gpt-5.6-astra`

cliproxyapi相关文档:
```bash
ntn pages get 3df4257d-2a6d-814b-91c3-d34c13ab2297
```

jev openrouter model id:
`typesafe/jev-1.13`
OpenRouter API key 环境变量：`OPEN_ROUTER_API_KEY`
example code:

```ts
import { OpenRouter } from "@openrouter/sdk";

const openrouter = new OpenRouter({
  apiKey: "<OPENROUTER_API_KEY>"
});

// The model answers narrow, typed questions about the state. Your code owns the workflow.
const decision = await openrouter.alpha.decisions.create({
  decisionsRequest: {
    model: "typesafe/jev-1.13",
    state: "Help! My payouts have been failing for 3 days.",
    questions: {
      is_urgent: {
        type: "noul",
        instructions: "Does this message convey urgency?",
        criteria: {
          true: "Explicitly time-sensitive",
          false: "No urgency expressed"
        }
      },
      department: {
        type: "choice",
        instructions: "Which team should handle this?",
        criteria: {
          billing: "Payments, invoicing, refunds",
          technical: "Bugs, outages, integrations",
          sales: "Pricing, upgrades, new accounts"
        }
      },
      frustration: {
        type: "score",
        instructions: "How frustrated is the customer?",
        criteria: ["Calm", "Frustrated", "Very angry"]
      }
    }
  }
});

const { is_urgent, department, frustration } = decision.answers;
if (is_urgent.type === "noul" && department.type === "choice" && frustration.type === "score") {
  // noul is a probability from 0 (no) to 1 (yes); choice and score carry the full distribution.
  console.log(is_urgent.noul, department.choice, department.probabilities, frustration.score);
  if (is_urgent.noul > 0.8 && department.choice === "billing") {
    // escalateToBilling(...)
  }
}
```
