import { Button as ButtonPrimitive } from "@base-ui/react/button"
import { mergeProps } from "@base-ui/react/merge-props"
import { useRender } from "@base-ui/react/use-render"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "cn"

// Odicea mockup `.btn`: the default action is "strong" (foreground on
// background), not the accent colour. Heights: sm 30 · default 36 · lg 44.
const buttonVariants = cva(
  "group/button inline-flex shrink-0 cursor-pointer items-center justify-center gap-1.5 rounded-md border border-transparent bg-clip-padding text-sm font-medium whitespace-nowrap transition-all outline-none select-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 active:not-aria-[haspopup]:translate-y-px disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        default: "bg-strong text-strong-foreground hover:opacity-88",
        primary: "bg-primary text-primary-foreground hover:bg-primary/85",
        outline:
          "border-border bg-background text-foreground hover:bg-muted aria-expanded:bg-muted",
        secondary:
          "bg-secondary text-secondary-foreground hover:bg-[color-mix(in_oklch,var(--secondary),var(--foreground)_6%)] aria-expanded:bg-secondary",
        ghost: "text-foreground hover:bg-muted aria-expanded:bg-muted",
        destructive:
          "border-destructive/50 bg-transparent text-destructive hover:bg-destructive/10 focus-visible:border-destructive/40 focus-visible:ring-destructive/20",
        "destructive-solid":
          "bg-destructive text-destructive-foreground hover:bg-destructive/85 focus-visible:ring-destructive/30",
        link: "h-auto! border-0 px-0! text-foreground underline-offset-4 hover:underline",
      },
      size: {
        default: "h-9 px-3.5 has-data-[icon=inline-end]:pr-3 has-data-[icon=inline-start]:pl-3",
        xs: "h-6 gap-1 rounded-sm px-2 text-xs [&_svg:not([class*='size-'])]:size-3",
        sm: "h-[30px] gap-1.5 px-2.5 text-[13px] [&_svg:not([class*='size-'])]:size-3.5",
        lg: "h-11 gap-2 px-5 text-[15px]",
        icon: "size-[34px]",
        "icon-xs": "size-6 rounded-sm [&_svg:not([class*='size-'])]:size-3",
        "icon-sm": "size-[30px] [&_svg:not([class*='size-'])]:size-3.5",
        "icon-lg": "size-11",
      },
      block: { true: "w-full" },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function Button({
  className,
  variant = "default",
  size = "default",
  block,
  ...props
}: ButtonPrimitive.Props & VariantProps<typeof buttonVariants>) {
  return (
    <ButtonPrimitive
      data-slot="button"
      className={cn(buttonVariants({ variant, size, block, className }))}
      {...props}
    />
  )
}

/**
 * Navigation that looks like a button. Use it (not `<Button render={<a/>}>`)
 * for anything that goes somewhere: Base UI gives a rendered anchor
 * role="button", and assistive tech would announce a link as a button.
 */
function ButtonLink({
  className,
  variant = "default",
  size = "default",
  block,
  render,
  ...props
}: Omit<useRender.ComponentProps<"a">, "className"> & { className?: string } & VariantProps<typeof buttonVariants>) {
  return useRender({
    defaultTagName: "a",
    render,
    props: mergeProps<"a">({ "data-slot": "button", className: cn(buttonVariants({ variant, size, block, className })) } as React.ComponentProps<"a">, props),
  })
}

export { Button, ButtonLink, buttonVariants }
