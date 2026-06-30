import * as React from "react"

import { cn } from "@/lib/utils"

// Styled native checkbox. Used heavily in the admin row editors.
function Checkbox({ className, ...props }: Omit<React.ComponentProps<"input">, "type">) {
  return (
    <input
      type="checkbox"
      data-slot="checkbox"
      className={cn(
        "size-4 shrink-0 rounded-[4px] border-input accent-primary outline-none focus-visible:ring-ring/50 focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-50",
        className
      )}
      {...props}
    />
  )
}

export { Checkbox }
