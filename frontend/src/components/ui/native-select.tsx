import * as React from "react"

import { cn } from "@/lib/utils"

// Lightweight styled native <select>. The admin tables render many inline
// per-row selects; a native control keeps that ergonomic while matching the
// shadcn input styling. (The Radix Select in ui/select.tsx remains available
// for richer popover menus.)
function NativeSelect({ className, ...props }: React.ComponentProps<"select">) {
  return (
    <select
      data-slot="native-select"
      className={cn(
        "border-input focus-visible:border-ring focus-visible:ring-ring/50 flex h-9 w-full min-w-0 rounded-md border bg-transparent px-3 py-1 text-sm shadow-xs transition-[color,box-shadow] outline-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-50",
        className
      )}
      {...props}
    />
  )
}

export { NativeSelect }
